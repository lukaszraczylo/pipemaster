package argo_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
	"github.com/lukaszraczylo/pipemaster/report"
)

func repo(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

// parseFiles parses a repo whose files sit under .argo/.
func parseFiles(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	in := map[string]string{}
	for n, b := range files {
		in[".argo/"+n] = b
	}
	p, rep, err := argo.New().Parse(repo(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, rep
}

func parseYAML(t *testing.T, y string) (*ir.Pipeline, *report.Report) {
	t.Helper()
	p, rep := parseFiles(t, map[string]string{"p.yaml": y})
	if len(p.Pipelines) != 1 {
		t.Fatalf("want one pipeline, got %d", len(p.Pipelines))
	}
	return p.Pipelines[0], rep
}

func parseErr(t *testing.T, y string) {
	t.Helper()
	if _, _, err := argo.New().Parse(repo(map[string]string{".argo/p.yaml": y})); err == nil {
		t.Fatal("Parse succeeded, want an error")
	}
}

func job(t *testing.T, pl *ir.Pipeline, id string) *ir.Job {
	t.Helper()
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %q not found", id)
	return nil
}

func ext(e ir.Ext) map[string]any {
	m, _ := e["argo"].(map[string]any)
	return m
}

func hasEntry(rep *report.Report, substr string) bool {
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, substr) {
			return true
		}
	}
	return false
}

func needIDs(j *ir.Job) []string {
	var out []string
	for _, n := range j.Needs {
		out = append(out, n.JobID)
	}
	return out
}

func ids(jobs []*ir.Job) []string {
	var out []string
	for _, j := range jobs {
		out = append(out, j.ID)
	}
	return out
}

func fmtAny(v any) string { return fmt.Sprint(v) }

// wt wraps template YAML in a WorkflowTemplate named ci whose entrypoint is main.
func wt(templates string) string {
	return "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: ci}\nspec:\n  entrypoint: main\n  templates:\n" + templates
}

// dagOf is a main dag over tasks plus the job templates.
func dagOf(tasks, templates string) string {
	return wt("  - name: main\n    dag:\n      tasks:\n" + tasks + templates)
}

const echoTemplates = `  - name: echo
    container: {image: alpine, command: [sh, -c], args: ["echo hi"]}
`

func cond(e expr.Expr) string {
	if e == nil {
		return ""
	}
	return expr.String(e)
}
