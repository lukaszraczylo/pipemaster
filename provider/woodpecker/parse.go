package woodpecker

import (
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	extStrategy = "kept in Ext[" + dialect + "]"
	pipelinePth = "pipeline/" + pipelineID
)

// Parse reads the workflows of the repo into one job per workflow. They form one pipeline with ID woodpecker,
// unless markers name the pipelines an emitter folded into them.
func (adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	d, err := discover(fsys)
	if err != nil {
		return nil, nil, err
	}
	if len(d.files) == 0 {
		return nil, nil, fmt.Errorf("woodpecker: no workflow found (looked for %s/*.yaml, %s, %s)", dirName, fileYAML, fileYML)
	}
	rep := &report.Report{Source: dialect}
	for _, f := range d.ignored {
		rep.Add(report.Entry{
			IRPath: pipelinePth, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "ignored",
			Detail: fmt.Sprintf("%s is ignored: Woodpecker reads only %s", f, d.files[0]), SourceLoc: f,
		})
	}
	var wfs []*workflow
	for _, f := range d.files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, nil, err
		}
		wf, err := parseWorkflow(rep, f, data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f, err)
		}
		wf.file = f
		wfs = append(wfs, wf)
	}
	var pls []*ir.Pipeline
	for _, g := range splitPipelines(wfs) {
		pl, err := g.build(rep)
		if err != nil {
			return nil, nil, err
		}
		pls = append(pls, pl)
	}
	proj := &ir.Project{
		Origin:    ir.Origin{Provider: dialect, Dialect: dialect, Files: d.files},
		Pipelines: pls,
	}
	if err := ir.Validate(proj); err != nil {
		return nil, nil, err
	}
	ir.Normalize(proj)
	return proj, rep, nil
}

// workflowName is the file name without leading dots and extension; depends_on uses it.
func workflowName(file string) string {
	n := strings.TrimLeft(path.Base(file), ".")
	n = strings.TrimSuffix(strings.TrimSuffix(n, ".yaml"), ".yml")
	return n
}

// sharedTriggers returns the pipeline triggers when every workflow has the same pure trigger filter, else nil:
// one pipeline holds all the jobs, so a trigger can only stand for a filter that every job shares.
func sharedTriggers(specs []*whenSpec) []ir.Trigger {
	var first []ir.Trigger
	for i, s := range specs {
		ts, ok := s.triggers()
		if !ok || (i > 0 && !reflect.DeepEqual(ts, first)) {
			return nil
		}
		first = ts
	}
	return first
}

// sink collects the keys a parser could not map; they become Ext[woodpecker] and each has a report entry.
type sink struct {
	rep  *report.Report
	vals map[string]any
	path string
	file string
}

func newSink(rep *report.Report, file, path string) *sink {
	return &sink{rep: rep, vals: map[string]any{}, path: path, file: file}
}

func (k *sink) note(f caps.Feature, detail string, line int) {
	loc := k.file
	if line > 0 {
		loc = fmt.Sprintf("%s:%d", k.file, line)
	}
	k.rep.Add(report.Entry{IRPath: k.path, Feature: f, Level: caps.Lost, Strategy: extStrategy, Detail: detail, SourceLoc: loc})
}

// keep stores v under key and reports why.
func (k *sink) keep(key string, v any, f caps.Feature, detail string, line int) {
	k.vals[key] = plain(v)
	k.note(f, detail, line)
}

// keepRest moves every key the reader did not consume to Ext.
func (k *sink) keepRest(r *reader, feats map[string]caps.Feature) {
	for _, key := range r.rest() {
		f := caps.ExtPassthrough
		if ff, ok := feats[key]; ok {
			f = ff
		}
		k.keep(key, r.m.vals[key], f, "key "+key+" has no IR field; kept in Ext", r.line(key))
	}
}

func (k *sink) ext() ir.Ext {
	if len(k.vals) == 0 {
		return nil
	}
	return ir.Ext{dialect: k.vals}
}

// sharedConcurrency sets the pipeline concurrency when every workflow has the same group. Otherwise each
// workflow keeps its key in Ext, as for any key the IR does not model.
func sharedConcurrency(rep *report.Report, pl *ir.Pipeline, wfs []*workflow, files []string) {
	same := len(wfs) > 0
	for _, w := range wfs {
		same = same && w.conc != nil && w.conc.ir == wfs[0].conc.ir
	}
	if same {
		pl.Concurrency = &ir.Concurrency{Group: wfs[0].conc.ir.Group}
		return
	}
	for i, w := range wfs {
		if w.conc == nil {
			continue
		}
		k := newSink(rep, files[i], pipelinePth+"/job/"+w.job.ID)
		if m, ok := w.job.Ext[dialect].(map[string]any); ok {
			k.vals = m
		}
		k.keep("concurrency", w.conc.raw, caps.PipelineConcurrency, "key concurrency has no IR field; kept in Ext", w.conc.line)
		w.job.Ext = k.ext()
	}
}
