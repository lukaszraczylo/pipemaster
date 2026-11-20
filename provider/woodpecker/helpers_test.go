package woodpecker_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func repo(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

// parseFiles parses a repo after checking every workflow against the pinned schema.
func parseFiles(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	for name, body := range files {
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			if err := validate.File("woodpecker", name, []byte(body)); err != nil {
				t.Fatalf("fixture %s is not valid Woodpecker YAML: %v", name, err)
			}
		}
	}
	return parseRaw(t, files)
}

func parseRaw(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	p, rep, err := woodpecker.New().Parse(repo(files))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, rep
}

func parseErr(t *testing.T, files map[string]string) error {
	t.Helper()
	_, _, err := woodpecker.New().Parse(repo(files))
	if err == nil {
		t.Fatal("Parse succeeded, want an error")
	}
	return err
}

func single(t *testing.T, yml string) (*ir.Pipeline, *ir.Job, *report.Report) {
	t.Helper()
	p, rep := parseFiles(t, map[string]string{".woodpecker.yaml": yml})
	if len(p.Pipelines) != 1 || len(p.Pipelines[0].Jobs) != 1 {
		t.Fatalf("want one pipeline with one job, got %+v", p)
	}
	return p.Pipelines[0], p.Pipelines[0].Jobs[0], rep
}

func step(t *testing.T, j *ir.Job, id string) *ir.Step {
	t.Helper()
	for _, s := range j.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found in %+v", id, j.Steps)
	return nil
}

func ext(m ir.Ext) map[string]any {
	v, _ := m["woodpecker"].(map[string]any)
	return v
}

func hasEntry(rep *report.Report, substr string) bool {
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, substr) || strings.Contains(e.Strategy, substr) {
			return true
		}
	}
	return false
}

func text(e expr.Expr) string {
	if e == nil {
		return ""
	}
	return expr.String(e)
}
