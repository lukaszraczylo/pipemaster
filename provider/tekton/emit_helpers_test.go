package tekton_test

import (
	"bytes"
	"io"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
	"github.com/lukaszraczylo/pipemaster/report"
)

// emit runs Emit on p, which must be a valid project, and returns the files as text.
func emit(t *testing.T, p *ir.Project) (map[string]string, *report.Report) {
	t.Helper()
	ir.Normalize(p)
	raw, rep, err := tekton.New().Emit(p)
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
	return &ir.Job{ID: id, Steps: []*ir.Step{{ID: id + "-run", Kind: ir.StepScript, Image: &ir.Image{Ref: "golang:1.27"}, Script: script}}}
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

func hasNamed(v any, name string) bool {
	for _, e := range list(v) {
		if m, ok := e.(doc); ok && m["name"] == name {
			return true
		}
	}
	return false
}

func pipelineDoc(t *testing.T, files map[string]string, name string) doc {
	t.Helper()
	for _, d := range docs(t, files[".tekton/pipeline.yaml"]) {
		if get(d, "metadata", "name") == name {
			return d
		}
	}
	t.Fatalf("no Pipeline %q in %s", name, files[".tekton/pipeline.yaml"])
	return nil
}

func taskDoc(t *testing.T, files map[string]string, name string) doc {
	t.Helper()
	src, ok := files[".tekton/tasks/"+name+".yaml"]
	if !ok {
		t.Fatalf("no task file for %q in %v", name, fileNames(files))
	}
	return docs(t, src)[0]
}

func fileNames(files map[string]string) []string {
	var out []string
	for n := range files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func entry(rep *report.Report, f caps.Feature, substr string) *report.Entry {
	for i := range rep.Entries {
		e := &rep.Entries[i]
		if e.Feature == f && strings.Contains(e.Detail+" "+e.Strategy+" "+e.IRPath, substr) {
			return e
		}
	}
	return nil
}

func wantEntry(t *testing.T, rep *report.Report, f caps.Feature, level caps.Level, substr string) {
	t.Helper()
	e := entry(rep, f, substr)
	if e == nil {
		t.Fatalf("no %s entry containing %q in\n%v", f, substr, rep.Entries)
	}
	if e.Level != level {
		t.Fatalf("%s entry %q is %v, want %v", f, substr, e.Level, level)
	}
}

func refExpr(path ...string) expr.Expr { return expr.Ref{Path: path} }

func eqStr(path []string, lit string) expr.Expr {
	return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: path}, R: expr.Lit{Kind: expr.LitStr, Str: lit}}
}

// reparse parses emitted files back into the IR.
func reparse(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	p, rep, err := tekton.New().Parse(repo(files))
	if err != nil {
		t.Fatalf("Parse of emitted files: %v\n%v", err, files)
	}
	return p, rep
}

func joinFiles(files map[string]string) string {
	var b bytes.Buffer
	for _, n := range fileNames(files) {
		b.WriteString("## " + n + "\n" + files[n])
	}
	return b.String()
}

// celOpaque is a Tekton cel when entry as the parser keeps it.
func celOpaque() expr.Expr {
	return expr.Opaque{Dialect: "tekton", Text: `{"cel":"'$(params.ctx-branch)'.matches('release/.*')"}`}
}
