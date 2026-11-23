package concourse

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

// knownTop lists the top-level keys that have an Ext key of their own.
var knownTop = []string{ExtResourceTypes, ExtVarSources, ExtGroups, ExtDisplay, ExtUserData, ExtPrototypes}

type srcFile struct {
	top  *omap
	name string
	data []byte
}

// Parse reads the YAML files under .concourse/ into one pipeline per file that has a jobs list.
func (adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	rep := &report.Report{Source: dialect}
	files, err := yamlFiles(fsys)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, rep, fmt.Errorf("concourse: no YAML file found under %s/", dirName)
	}
	var pipes []srcFile
	others := map[string]any{}
	var otherNames []string
	for _, f := range files {
		top, data, err := readFile(fsys, f, rep)
		if err != nil {
			return nil, nil, err
		}
		switch {
		case top == nil:
		case isPipeline(top):
			pipes = append(pipes, srcFile{name: f, top: top, data: data})
		default:
			others[f] = plain(top)
			otherNames = append(otherNames, f)
		}
	}
	if len(pipes) == 0 {
		return nil, rep, fmt.Errorf("concourse: no pipeline with a jobs list found under %s/", dirName)
	}
	var pls []*ir.Pipeline
	byID := map[string]string{}
	for _, src := range pipes {
		id := strings.TrimSuffix(path.Base(src.name), path.Ext(src.name))
		if prev, dup := byID[id]; dup {
			return nil, nil, fmt.Errorf("concourse: %s and %s both name pipeline %q", prev, src.name, id)
		}
		byID[id] = src.name
		pb := newPipelineBuilder(rep, src.name, id)
		pb.readMarks(src.data)
		pb.anchorKeys = anchorHolders(src.data)
		pl, err := pb.build(src.top)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", src.name, err)
		}
		pls = append(pls, pl)
	}
	keepOtherFiles(rep, pls[0], others, otherNames)
	proj := &ir.Project{Origin: ir.Origin{Provider: dialect, Dialect: dialect, Files: files}, Pipelines: pls}
	if err := ir.Validate(proj); err != nil {
		return nil, nil, err
	}
	ir.Normalize(proj)
	return proj, rep, nil
}

// readFile decodes one file with the fly-compatible decoder; an empty file is nil. A document that is no mapping is an error.
func readFile(fsys fs.FS, file string, rep *report.Report) (*omap, []byte, error) {
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return nil, nil, err
	}
	node, err := DecodeNode(data)
	if errors.Is(err, ErrNoDocument) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	b := &ynode.Builder{}
	v, err := b.Build(node, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	for _, l := range b.Skipped {
		rep.Add(report.Entry{
			IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "dropped",
			Detail: "a merge key (<<) whose value is not a mapping adds no key and is dropped", SourceLoc: fmt.Sprintf("%s:%d", file, l),
		})
	}
	if sc, isScal := v.(ynode.Scal); v == nil || (isScal && sc.Null()) {
		return nil, nil, nil
	}
	top, ok := asMap(v)
	if !ok {
		return nil, nil, fmt.Errorf("%s: a Concourse file must be a mapping", file)
	}
	return top, data, nil
}

func isPipeline(top *omap) bool {
	l, ok := top.Vals[keyJobs].([]any)
	return ok && len(l) > 0
}

// keepOtherFiles keeps the files that are no pipeline (task files, vars files) in the Ext of the first pipeline.
func keepOtherFiles(rep *report.Report, pl *ir.Pipeline, others map[string]any, names []string) {
	if len(others) == 0 {
		return
	}
	sort.Strings(names)
	for _, n := range names {
		rep.Add(report.Entry{
			IRPath: "pipeline/" + pl.ID, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: strategyExt,
			Detail: n + " has no jobs list, so it is no pipeline; the file is kept in Ext", SourceLoc: n,
		})
	}
	extMap(pl)[ExtFiles] = others
}

const strategyExt = "kept in Ext[" + dialect + "]"

// extMap returns the Concourse Ext map of a pipeline, creating it.
func extMap(pl *ir.Pipeline) map[string]any {
	if pl.Ext == nil {
		pl.Ext = ir.Ext{dialect: map[string]any{}}
	}
	return pl.Ext[dialect].(map[string]any)
}

// pipeBuilder is the state of one pipeline file.
type pipeBuilder struct {
	marks   map[ir.FeatureUse][]string
	bare    map[ir.FeatureUse]bool
	present map[string]bool
	// anchorKeys holds the top-level keys whose value defines an anchor that the file aliases elsewhere.
	anchorKeys map[string]bool
	// plainVars holds the names that a set_pipeline step passes in vars, so they are plain values.
	plainVars map[string]bool
	// defaulted holds the names of the params that were read as secrets with no marker or vars entry to say otherwise.
	defaulted map[string]bool
	rep       *report.Report
	res       map[string]*resource
	types     map[string]bool
	used      map[string]bool
	jobSet    map[string]bool
	file      string
	id        string
	resOrder  []*resource
	jobNames  []string
}

func newPipelineBuilder(rep *report.Report, file, id string) *pipeBuilder {
	return &pipeBuilder{rep: rep, file: file, id: id, res: map[string]*resource{}, types: map[string]bool{}, used: map[string]bool{}, jobSet: map[string]bool{}, present: map[string]bool{}, plainVars: map[string]bool{}, defaulted: map[string]bool{}}
}

func (b *pipeBuilder) path() string { return "pipeline/" + b.id }

func (b *pipeBuilder) loc(line int) string {
	if line > 0 {
		return fmt.Sprintf("%s:%d", b.file, line)
	}
	return b.file
}

func (b *pipeBuilder) note(f caps.Feature, level caps.Level, path, strategy, detail string, line int) {
	b.rep.Add(report.Entry{IRPath: path, Feature: f, Level: level, Strategy: strategy, Detail: detail, SourceLoc: b.loc(line)})
}

func (b *pipeBuilder) build(top *omap) (*ir.Pipeline, error) {
	pl := &ir.Pipeline{ID: b.id}
	k := newSink(b.rep, b.file, b.path())
	r := newReader(top)
	b.pipelineName(pl)
	jobs, _ := r.Take(keyJobs)
	b.readResources(r)
	b.readTypes(r)
	list, _ := jobs.([]any)
	rawList := list
	if b.placeholder(list) {
		list = nil
	}
	for i, raw := range list {
		m, ok := asMap(raw)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return nil, fmt.Errorf("job #%d needs a name", i+1)
		}
		b.jobNames = append(b.jobNames, name)
		b.jobSet[name] = true
	}
	collectSetPipelineVars(list, b.plainVars)
	var built []*builtJob
	for _, raw := range list {
		bj := b.job(raw.(*omap))
		built = append(built, bj)
		pl.Jobs = append(pl.Jobs, bj.job)
	}
	b.triggers(pl, built)
	b.concurrency(pl, built)
	b.applyJobNames(pl)
	b.carryPipeline(top, rawList, pl)
	b.finallyHooks(pl, built)
	b.orphanNames()
	b.secretNote()
	b.keepResources(k)
	for _, key := range knownTop {
		if v, ok := r.Take(key); ok {
			k.Keep(key, v, caps.ExtPassthrough, "top-level key "+key+" has no IR form; kept in Ext", r.Line(key))
		}
	}
	extra := map[string]any{}
	var holders []any
	for _, key := range r.Rest() {
		extra[key] = plain(top.Vals[key])
		if b.anchorKeys[key] {
			holders = append(holders, key)
			k.Note(caps.ExtPassthrough, "top-level key "+key+" only holds YAML anchors that the file aliases; kept in Ext", r.Line(key))
			continue
		}
		k.Note(caps.ExtPassthrough, "top-level key "+key+" is unknown to Concourse; kept in Ext", r.Line(key))
	}
	if len(extra) > 0 {
		k.Out[ExtExtra] = extra
	}
	if len(holders) > 0 {
		k.Out[ExtAnchorKeys] = holders
	}
	if len(k.Out) > 0 {
		pl.Ext = k.Ext()
	}
	return pl, nil
}

// collectSetPipelineVars adds the keys of every set_pipeline vars map in the plans to out.
func collectSetPipelineVars(v any, out map[string]bool) {
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			collectSetPipelineVars(x, out)
		}
	case *omap:
		if _, has := t.Vals["set_pipeline"]; has {
			if vars, ok := asMap(t.Vals["vars"]); ok {
				for _, k := range vars.Order {
					out[k] = true
				}
			}
		}
		for _, k := range t.Order {
			collectSetPipelineVars(t.Vals[k], out)
		}
	}
}

// secretNote says once per pipeline that params were read as secrets. A file that pipemaster wrote has its
// markers for the plain vars, so only a file with no markers can have lost that distinction.
func (b *pipeBuilder) secretNote() {
	if len(b.defaulted) == 0 || len(b.marks)+len(b.bare) > 0 {
		return
	}
	names := make([]string, 0, len(b.defaulted))
	for n := range b.defaulted {
		names = append(names, n)
	}
	sort.Strings(names)
	b.note(caps.ConfigName, caps.Emulated, b.path(), "every ((name)) is a secret",
		"Concourse resolves every ((name)) from the credential manager, so the params "+strings.Join(names, ", ")+
			" are treated as secrets, plain vars included; recreate a plain value as a plain variable in the target if it is no secret", 0)
}
