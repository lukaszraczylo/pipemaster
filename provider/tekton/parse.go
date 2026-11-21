package tekton

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const fallbackPipelineID = "tekton"

// pipeSrc is a pipeline definition: a Pipeline document, or the pipelineSpec of a PipelineRun.
type pipeSrc struct {
	doc  *rawDoc
	meta *omap
	spec *omap
	// run is the PipelineRun document when the pipeline is embedded in one.
	run  *rawDoc
	name string
}

// project is the state of one Parse call.
type project struct {
	rep       *report.Report
	tasks     map[string]*rawDoc
	taskUsed  map[string]bool
	templates map[string]*rawDoc
	trigDocs  map[string]*rawDoc
	trigUsed  map[string]bool
	pipes     []*pipeSrc
	listeners []*rawDoc
	crons     []*rawDoc
	resources []*rawDoc
	// kept is the documents that Ext holds, in file order.
	kept []*rawDoc
}

// Parse reads the YAML files under .tekton/ into one pipeline per Pipeline or embedded PipelineRun.
func (adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	rep := &report.Report{Source: dialect}
	files, err := yamlFiles(fsys)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, rep, fmt.Errorf("tekton: no YAML file found under %s/", dirName)
	}
	p := &project{
		rep: rep, tasks: map[string]*rawDoc{}, taskUsed: map[string]bool{}, templates: map[string]*rawDoc{},
		trigDocs: map[string]*rawDoc{}, trigUsed: map[string]bool{},
	}
	for _, f := range files {
		docs, err := readDocs(fsys, f, rep)
		if err != nil {
			return nil, nil, err
		}
		for _, d := range docs {
			p.classify(d)
		}
	}
	if len(p.pipes) == 0 {
		return nil, rep, fmt.Errorf("tekton: no Pipeline or PipelineRun with a pipelineSpec found under %s/", dirName)
	}
	var pls []*ir.Pipeline
	byName := map[string]*ir.Pipeline{}
	for _, src := range p.pipes {
		pl, err := p.pipeline(src)
		if err != nil {
			return nil, nil, err
		}
		pls = append(pls, pl)
		byName[src.name] = pl
	}
	p.triggers(byName)
	p.cronJobs(byName)
	for name, d := range p.tasks {
		if !p.taskUsed[name] {
			p.resources = append(p.resources, d)
		}
	}
	for name, d := range p.trigDocs {
		if !p.trigUsed[name] {
			p.resources = append(p.resources, d)
		}
	}
	sort.SliceStable(pls, func(i, j int) bool { return pls[i].ID < pls[j].ID })
	p.dropGenerated()
	p.attachResources(pls[0])
	proj := &ir.Project{
		Origin:    ir.Origin{Provider: dialect, Dialect: dialect, Files: files},
		Pipelines: pls,
	}
	if err := ir.Validate(proj); err != nil {
		return nil, nil, err
	}
	ir.Normalize(proj)
	return proj, rep, nil
}

func apiIs(d *rawDoc, prefix string) bool { return strings.HasPrefix(d.api, prefix) }

// classify sorts a document into the roles the parser uses; a document with no role stays in Ext.
func (p *project) classify(d *rawDoc) {
	meta, _ := asMap(d.top.vals["metadata"])
	name, _ := text(valueOf(meta, "name"))
	switch {
	case apiIs(d, apiPipelines) && d.kind == kindPipeline:
		if spec, ok := asMap(d.top.vals["spec"]); ok {
			p.pipes = append(p.pipes, &pipeSrc{doc: d, meta: meta, spec: spec, name: name})
			return
		}
		p.pipes = append(p.pipes, &pipeSrc{doc: d, meta: meta, name: name})
	case apiIs(d, apiPipelines) && d.kind == kindTask && name != "":
		if first, dup := p.tasks[name]; dup {
			p.rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: strategyExt,
				Detail: "Task " + name + " is defined twice; the first (" + first.loc(0) + ") is used and the later document is kept in Ext", SourceLoc: d.loc(0),
			})
			p.resources = append(p.resources, d)
			return
		}
		p.tasks[name] = d
	case apiIs(d, apiPipelines) && d.kind == kindPipelineRun:
		spec, _ := asMap(d.top.vals["spec"])
		if ps, ok := asMap(valueOf(spec, "pipelineSpec")); ok {
			if name == "" {
				gen, _ := text(valueOf(meta, "generateName"))
				name = strings.TrimRight(gen, "-")
			}
			p.pipes = append(p.pipes, &pipeSrc{doc: d, meta: meta, spec: ps, run: d, name: name})
			return
		}
		p.resources = append(p.resources, d)
	case apiIs(d, apiTriggers) && d.kind == kindEventListener:
		p.listeners = append(p.listeners, d)
	case apiIs(d, apiTriggers) && d.kind == kindTrigger && name != "":
		p.trigDocs[name] = d
	case apiIs(d, apiTriggers) && d.kind == kindTriggerTemplate && name != "":
		p.templates[name] = d
		p.resources = append(p.resources, d)
	case apiIs(d, apiBatch) && d.kind == kindCronJob:
		p.crons = append(p.crons, d)
	case d.kind == kindKustomization && strings.HasPrefix(d.api, kustomizeAPI) && p.isIndex(d):
		p.rep.Add(report.Entry{
			IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Emulated, Strategy: "kustomization index ignored",
			Detail: "the kustomization lists the files of the project and holds no pipeline data; it is written again on emit", SourceLoc: d.loc(0),
		})
	default:
		p.resources = append(p.resources, d)
	}
}

// isIndex tells the kustomization that the emitter writes from a hand-made one: it has only apiVersion, kind and resources.
func (p *project) isIndex(d *rawDoc) bool {
	if d.file != dirName+"/kustomization.yaml" {
		return false
	}
	for _, k := range d.top.keys {
		if k != "apiVersion" && k != "kind" && k != "resources" {
			return false
		}
	}
	return true
}

func valueOf(m *omap, key string) any {
	if m == nil {
		return nil
	}
	return m.vals[key]
}

// attachResources keeps the documents with no IR field in the Ext of the first pipeline, each with an entry.
func (p *project) attachResources(pl *ir.Pipeline) {
	if len(p.resources) == 0 {
		return
	}
	sort.SliceStable(p.resources, func(i, j int) bool {
		a, b := p.resources[i], p.resources[j]
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if an, bn := a.name(), b.name(); an != bn {
			return an < bn
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.line < b.line
	})
	var list []any
	for _, d := range p.resources {
		list = append(list, d.ext())
		kind := d.kind
		if kind == "" {
			kind = "(none)"
		}
		p.rep.Add(report.Entry{
			IRPath: "pipeline/" + pl.ID, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: strategyExt,
			Detail: "kind: " + kind + " has no IR form; the document is kept in Ext", SourceLoc: d.loc(0),
		})
	}
	if pl.Ext == nil {
		pl.Ext = ir.Ext{dialect: map[string]any{}}
	}
	pl.Ext[dialect].(map[string]any)[ExtResources] = list
}

// ext is the form of the document that Ext holds.
func (d *rawDoc) ext() any {
	if d.view != nil {
		return d.view
	}
	return plain(d.top)
}

// name is the metadata name of the document, empty when it has none.
func (d *rawDoc) name() string {
	md, _ := valueOf(d.top, "metadata").(*omap)
	n, _ := text(valueOf(md, "name"))
	return n
}
