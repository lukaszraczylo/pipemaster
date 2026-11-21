package tekton_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
	"github.com/lukaszraczylo/pipemaster/report"
)

func repo(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

// parseFiles parses a repo whose files sit under .tekton/.
func parseFiles(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	in := map[string]string{}
	for n, b := range files {
		in[".tekton/"+n] = b
	}
	p, rep, err := tekton.New().Parse(repo(in))
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
	if _, _, err := tekton.New().Parse(repo(map[string]string{".tekton/p.yaml": y})); err == nil {
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

func step(t *testing.T, j *ir.Job, id string) *ir.Step {
	t.Helper()
	for _, s := range j.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found in %s", id, j.ID)
	return nil
}

func ext(e ir.Ext) map[string]any {
	m, _ := e["tekton"].(map[string]any)
	return m
}

func hasEntry(rep *report.Report, substr string) bool {
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, substr) || strings.Contains(e.Strategy, substr) {
			return true
		}
	}
	return false
}

func cond(e expr.Expr) string {
	if e == nil {
		return ""
	}
	return expr.String(e)
}

// pipelineWith wraps tasks in a v1 Pipeline named ci.
func pipelineWith(spec string) string {
	return "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  name: ci\nspec:\n" + spec
}

// inlineTask is a PipelineTask with an inline taskSpec.
const inlineTask = "  tasks:\n  - name: build\n    taskSpec:\n      steps:\n      - name: s\n        image: golang\n        script: go test ./...\n"

func fmtAny(v any) string { return fmt.Sprint(v) }
