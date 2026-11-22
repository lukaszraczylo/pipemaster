package argo_test

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
	"github.com/lukaszraczylo/pipemaster/report"
)

// emit runs Emit on a valid project and returns the files as text.
func emit(t *testing.T, p *ir.Project) (map[string]string, *report.Report) {
	t.Helper()
	ir.Normalize(p)
	raw, rep, err := argo.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := map[string]string{}
	for n, b := range raw {
		out[n] = string(b)
	}
	return out, rep
}

// project makes a one-pipeline project from a foreign origin.
func project(jobs ...*ir.Job) *ir.Project {
	return &ir.Project{
		Origin:    ir.Origin{Provider: "gha", Dialect: "gha"},
		Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: jobs}},
	}
}

func scriptJob(id string, script ...string) *ir.Job {
	return &ir.Job{ID: id, Steps: []*ir.Step{{ID: id + "-run", Kind: ir.StepScript, Script: script}}}
}

type doc = map[string]any

// docs decodes a multi-document file.
func docs(t *testing.T, src string) []doc {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(src))
	var out []doc
	for {
		var d doc
		if err := dec.Decode(&d); err != nil {
			if err == io.EOF {
				return out
			}
			t.Fatalf("decode: %v\n%s", err, src)
		}
		out = append(out, d)
	}
}

func get(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(doc)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func list(v any) []any { l, _ := v.([]any); return l }

// named finds the mapping with the given name in a list.
func named(t *testing.T, v any, name string) doc {
	t.Helper()
	for _, e := range list(v) {
		if m, ok := e.(doc); ok && m["name"] == name {
			return m
		}
	}
	t.Fatalf("no entry named %q in %v", name, v)
	return nil
}

func joinFiles(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString("--- " + n + "\n" + files[n])
	}
	return sb.String()
}

// roundTrip parses the emitted files back.
func roundTrip(t *testing.T, files map[string]string) *ir.Project {
	t.Helper()
	p, _, err := argo.New().Parse(repo(files))
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, joinFiles(files))
	}
	return p
}

func entries(rep *report.Report, sub string) []report.Entry {
	var out []report.Entry
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, sub) || strings.Contains(e.Strategy, sub) {
			out = append(out, e)
		}
	}
	return out
}

// stripExt clears every Ext, so a foreign project compares with the IR that it makes after one trip.
func stripExt(p *ir.Project) {
	for _, pl := range p.Pipelines {
		pl.Ext = nil
		for i := range pl.Triggers {
			pl.Triggers[i].Ext = nil
		}
		for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
			j.Ext = nil
			for _, st := range j.Steps {
				st.Ext = nil
			}
		}
	}
}

func eqStr(path []string, lit string) expr.Expr {
	return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: path}, R: expr.Lit{Kind: expr.LitStr, Str: lit}}
}

func parseRepo(files map[string]string) (*ir.Project, *report.Report, error) {
	return argo.New().Parse(repo(files))
}

// joinExt is the text of the Ext of a job and its steps.
func joinExt(j *ir.Job) string {
	s := fmt.Sprint(j.Ext)
	for _, st := range j.Steps {
		s += fmt.Sprint(st.Ext)
	}
	return s
}

func joinExtP(p *ir.Project) string {
	var s string
	for _, pl := range p.Pipelines {
		s += fmt.Sprint(pl.Ext)
	}
	return s
}
