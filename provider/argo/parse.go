package argo

import (
	"fmt"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

// pipeSrc is a pipeline definition: a workflow template, a Workflow with inline templates, or the workflowSpec of a CronWorkflow.
type pipeSrc struct {
	doc  *rawDoc
	meta *omap
	spec *omap
	// cron is the CronWorkflow document when the pipeline is its workflowSpec.
	cron *rawDoc
	// tmpls indexes the templates of the spec by name; used says which of them a job or handler read.
	tmpls map[string]*omap
	used  map[string]bool
	name  string
}

// project is the state of one Parse call.
type project struct {
	rep     *report.Report
	wts     map[string]*pipeSrc
	pipes   []*pipeSrc
	runs    []*rawDoc
	crons   []*rawDoc
	sensors []*rawDoc
	sources map[string]*rawDoc
	// srcUsed lists the event entries of each EventSource that a trigger consumed, as "type/event".
	srcUsed map[string]map[string]bool
	// resources is the documents that Ext holds, in file order.
	resources []*rawDoc
}

// Parse reads the YAML files under .argo/ into one pipeline per workflow definition.
func (adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	proj, rep, err := adapter{}.parseSource(fsys)
	vars.Strip(proj)
	emitkit.DropSourceCheckout(proj)
	return proj, rep, err
}

func (adapter) parseSource(fsys fs.FS) (*ir.Project, *report.Report, error) {
	rep := &report.Report{Source: dialect}
	files, err := yamlFiles(fsys)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, rep, fmt.Errorf("argo: no YAML file found under %s/", dirName)
	}
	p := &project{rep: rep, wts: map[string]*pipeSrc{}, sources: map[string]*rawDoc{}, srcUsed: map[string]map[string]bool{}}
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, nil, err
		}
		if isHelm(data) {
			rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "skipped", SourceLoc: f,
				Detail: f + " holds Helm template text and cannot be read until Helm renders it; the file is skipped",
			})
			continue
		}
		docs, err := ynode.ReadDocsData(data, f, rep)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range docs {
			p.classify(d)
		}
	}
	if len(p.pipes) == 0 && len(p.runs) == 0 {
		return nil, rep, fmt.Errorf("argo: no workflow definition with templates found under %s/", dirName)
	}
	var pls []*ir.Pipeline
	byName := map[string]*ir.Pipeline{}
	byCron := map[*rawDoc]*ir.Pipeline{}
	srcOf := map[*ir.Pipeline]*pipeSrc{}
	p.renameClashes()
	var libs []*pipeSrc
	for _, src := range p.pipes {
		if isLibrary(src) {
			libs = append(libs, src)
			continue
		}
		pl, err := p.pipeline(src)
		if err != nil {
			return nil, nil, err
		}
		pls = append(pls, pl)
		srcOf[pl] = src
		if src.cron == nil {
			byName[src.name] = pl
		} else {
			byCron[src.cron] = pl
		}
	}
	if len(pls) == 0 {
		pl := p.libraryPipeline(libs)
		if pl == nil {
			return nil, rep, fmt.Errorf("argo: no workflow with an entrypoint found under %s/", dirName)
		}
		pls = append(pls, pl)
	}
	p.keepLibraries(libs)
	p.runBindings(byName)
	p.cronTriggers(byName, byCron)
	p.sensorTriggers(byName)
	p.manualMarkers(pls, srcOf)
	sort.SliceStable(pls, func(i, j int) bool { return pls[i].ID < pls[j].ID })
	p.attachResources(pls[0])
	proj := &ir.Project{Origin: ir.Origin{Provider: dialect, Dialect: dialect, Files: files}, Pipelines: pls}
	if err := ir.Validate(proj); err != nil {
		return nil, nil, err
	}
	ir.Normalize(proj)
	return proj, rep, nil
}

func hasAPI(d *rawDoc) bool { return strings.HasPrefix(d.API, apiArgo) }

// newPipeSrc indexes the templates of a spec.
func newPipeSrc(d *rawDoc, meta, spec *omap, name string) *pipeSrc {
	src := &pipeSrc{doc: d, meta: meta, spec: spec, name: name, tmpls: map[string]*omap{}, used: map[string]bool{}}
	list, _ := valueOf(spec, "templates").([]any)
	for _, e := range list {
		if m, ok := asMap(e); ok {
			if n, ok := text(m.Vals["name"]); ok {
				if _, dup := src.tmpls[n]; !dup {
					src.tmpls[n] = m
				}
			}
		}
	}
	return src
}

// nameOf is the name of a document, or its generateName without the trailing dash.
func nameOf(meta *omap) string {
	if n, ok := text(valueOf(meta, "name")); ok {
		return n
	}
	gen, _ := text(valueOf(meta, "generateName"))
	return strings.TrimRight(gen, "-")
}

// classify sorts a document into the roles the parser uses; a document with no role stays in Ext.
func (p *project) classify(d *rawDoc) {
	meta, _ := asMap(d.Top.Vals["metadata"])
	spec, _ := asMap(d.Top.Vals["spec"])
	name := nameOf(meta)
	if p.isGeneratedRBAC(d) {
		return
	}
	if !hasAPI(d) {
		p.resources = append(p.resources, d)
		return
	}
	switch d.Kind {
	case kindTemplate, kindClusterTmpl:
		if spec == nil || name == "" {
			p.resources = append(p.resources, d)
			return
		}
		if first, dup := p.wts[name]; dup {
			p.rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "kept in Ext[" + dialect + "]", SourceLoc: d.Loc(0),
				Detail: d.Kind + " " + name + " is defined twice; the first (" + first.doc.Loc(0) + ") is used and the later document is kept in Ext",
			})
			p.resources = append(p.resources, d)
			return
		}
		src := newPipeSrc(d, meta, spec, name)
		p.wts[name] = src
		p.pipes = append(p.pipes, src)
	case kindWorkflow:
		switch {
		case spec != nil && len(listOf(valueOf(spec, "templates"))) > 0:
			p.pipes = append(p.pipes, newPipeSrc(d, meta, spec, name))
		case spec != nil && refName(spec) != "":
			p.runs = append(p.runs, d)
		default:
			p.resources = append(p.resources, d)
		}
	case kindCron:
		p.crons = append(p.crons, d)
		if ws, _ := asMap(valueOf(spec, "workflowSpec")); ws != nil && len(listOf(ws.Vals["templates"])) > 0 {
			src := newPipeSrc(d, nil, ws, name)
			src.cron = d
			p.pipes = append(p.pipes, src)
		}
	case kindSensor:
		p.sensors = append(p.sensors, d)
	case kindEventSource:
		if name != "" && p.sources[name] == nil {
			p.sources[name] = d
		} else {
			p.resources = append(p.resources, d)
		}
	default:
		p.resources = append(p.resources, d)
	}
}

// refName is the workflowTemplateRef name of a workflow spec.
func refName(spec *omap) string {
	ref, _ := asMap(valueOf(spec, "workflowTemplateRef"))
	n, _ := text(valueOf(ref, "name"))
	return n
}

// attachResources keeps the documents with no IR field in the Ext of the first pipeline, each with an entry.
func (p *project) attachResources(pl *ir.Pipeline) {
	for name, d := range p.sources {
		if p.srcUsed[name] == nil {
			p.resources = append(p.resources, d)
			continue
		}
		if view := unusedEvents(d, p.srcUsed[name]); view != nil {
			cp := *d
			cp.View = view
			p.resources = append(p.resources, &cp)
		}
	}
	if len(p.resources) == 0 {
		return
	}
	sort.SliceStable(p.resources, func(i, j int) bool {
		a, b := p.resources[i], p.resources[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if an, bn := a.Name(), b.Name(); an != bn {
			return an < bn
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	var list []any
	for _, d := range p.resources {
		list = append(list, d.Ext())
		kind := d.Kind
		if kind == "" {
			kind = "(none)"
		}
		p.rep.Add(report.Entry{
			IRPath: "pipeline/" + pl.ID, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "kept in Ext[" + dialect + "]",
			Detail: "kind: " + kind + " has no IR form; the document " + d.Name() + " is kept in Ext", SourceLoc: d.Loc(0),
		})
	}
	if pl.Ext == nil {
		pl.Ext = ir.Ext{dialect: map[string]any{}}
	}
	pl.Ext[dialect].(map[string]any)[ExtResources] = list
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

// keepLibraries keeps each library WorkflowTemplate in Ext, so its metadata and spec keys are not lost.
// libraryID is the ID of the pipeline that holds a project with templates or runs and no workflow of its own.
const libraryID = "library"

// libraryPipeline stands in for a project of library templates and run bindings only. The IR needs a pipeline to
// hold Ext, so the pipeline has no job; its documents stay in Ext and other providers report them as lost.
func (p *project) libraryPipeline(libs []*pipeSrc) *ir.Pipeline {
	if len(libs) == 0 && len(p.runs) == 0 {
		return nil
	}
	taken := map[string]bool{}
	for _, src := range p.pipes {
		taken[src.name] = true
	}
	for _, d := range p.runs {
		taken[d.Name()] = true
	}
	id := libraryID
	for n := 2; taken[id]; n++ {
		id = libraryID + "-" + strconv.Itoa(n)
	}
	p.rep.Add(report.Entry{
		IRPath: "pipeline/" + id, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "kept in Ext[" + dialect + "]",
		Detail: "no workflow with an entrypoint exists; the templates and runs are kept in Ext of the pipeline " + id + ", which has no job",
	})
	return &ir.Pipeline{ID: id}
}

func (p *project) keepLibraries(libs []*pipeSrc) {
	for _, src := range libs {
		p.resources = append(p.resources, src.doc)
	}
}

const clashSuffix = "-workflow"

// renameClashes gives a Workflow, or a CronWorkflow with its own templates, a new pipeline ID when its name
// matches a WorkflowTemplate or an earlier pipeline. Argo keeps one namespace per kind, so the names are valid.
func (p *project) renameClashes() {
	taken := map[string]bool{}
	for _, src := range p.pipes {
		if p.wts[src.name] == src {
			taken[src.name] = true
		}
	}
	for _, src := range p.pipes {
		if p.wts[src.name] == src {
			continue
		}
		id := src.name
		for n := 1; taken[id]; n++ {
			id = src.name + clashSuffix
			if n > 1 {
				id += "-" + strconv.Itoa(n)
			}
		}
		taken[id] = true
		if id == src.name {
			continue
		}
		p.rep.Add(report.Entry{
			IRPath: "pipeline/" + id, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "renamed",
			Detail: src.doc.Kind + " " + src.name + " shares its name with another pipeline; its pipeline ID is " + id, SourceLoc: src.doc.Loc(0),
		})
		src.name = id
	}
}
