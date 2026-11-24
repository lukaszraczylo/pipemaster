package drone

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const (
	extStrategy = "kept in Ext[" + dialect + "]"
	pipelinePth = "pipeline/" + pipelineID

	kindPipeline  = "pipeline"
	kindSignature = "signature"
	typeDocker    = "docker"

	// ExtResources is the pipeline Ext key that holds the documents of every kind but pipeline, in file order.
	ExtResources = "resources"
)

// Parse reads .drone.yml into one pipeline, drone, with one job per kind: pipeline document.
func (adapter) Parse(fsys fs.FS) (*ir.Project, *report.Report, error) {
	proj, rep, err := adapter{}.parseSource(fsys)
	vars.Strip(proj)
	return proj, rep, err
}

func (adapter) parseSource(fsys fs.FS) (*ir.Project, *report.Report, error) {
	rep := &report.Report{Source: dialect}
	for _, f := range scripted {
		if regular(fsys, f) {
			rep.Add(report.Entry{
				IRPath: pipelinePth, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "unsupported",
				Detail: f + " is a generator file; the parser reads only " + fileYML, SourceLoc: f,
			})
		}
	}
	if !regular(fsys, fileYML) {
		return nil, rep, fmt.Errorf("drone: no %s found (the parser does not run %s)", fileYML, strings.Join(scripted, " or "))
	}
	data, err := fs.ReadFile(fsys, fileYML)
	if err != nil {
		return nil, nil, err
	}
	res := newSink(rep, fileYML, pipelinePth, 0)
	var docs []*doc
	for _, c := range splitDocuments(string(data)) {
		d, res1, err := parseChunk(rep, c)
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", fileYML, c.start, err)
		}
		switch {
		case d != nil:
			docs = append(docs, d)
		case res1 != nil:
			res.vals[ExtResources] = append(listOf(res.vals[ExtResources]), res1.value)
		}
	}
	pls, err := buildPipelines(rep, docs)
	if err != nil {
		return nil, nil, err
	}
	pls[0].Ext = res.ext()
	proj := &ir.Project{
		Origin:    ir.Origin{Provider: dialect, Dialect: dialect, Files: []string{fileYML}},
		Pipelines: pls,
	}
	if err := ir.Validate(proj); err != nil {
		return nil, nil, err
	}
	ir.Normalize(proj)
	return proj, rep, nil
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

// sink collects the keys a parser could not map; they become Ext[drone] and each has a report entry.
type sink struct {
	rep  *report.Report
	vals map[string]any
	path string
	file string
	// base is the number of lines before the document, so entries name file lines.
	base int
}

func newSink(rep *report.Report, file, path string, base int) *sink {
	return &sink{rep: rep, vals: map[string]any{}, path: path, file: file, base: base}
}

func (k *sink) note(f caps.Feature, detail string, line int) {
	loc := k.file
	if line > 0 {
		loc = fmt.Sprintf("%s:%d", k.file, k.base+line)
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
		detail := "key " + key + " has no IR field; kept in Ext"
		if key == "depends_on" {
			detail += "; step order follows the file and the step DAG is not applied"
		}
		k.keep(key, r.m.vals[key], f, detail, r.line(key))
	}
}

func (k *sink) ext() ir.Ext {
	if len(k.vals) == 0 {
		return nil
	}
	return ir.Ext{dialect: k.vals}
}
