package drone_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
	"github.com/lukaszraczylo/pipemaster/report"
)

const minimal = "kind: pipeline\nname: build\nsteps:\n- name: test\n  image: golang\n  commands:\n  - go test ./...\n"

func repo(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func parseYML(t *testing.T, yml string) (*ir.Pipeline, *report.Report) {
	t.Helper()
	p, rep, err := drone.New().Parse(repo(map[string]string{".drone.yml": yml}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(p.Pipelines) != 1 {
		t.Fatalf("want one pipeline, got %d", len(p.Pipelines))
	}
	return p.Pipelines[0], rep
}

func parseJob(t *testing.T, yml string) (*ir.Job, *report.Report) {
	t.Helper()
	pl, rep := parseYML(t, yml)
	if len(pl.Jobs) != 1 {
		t.Fatalf("want one job, got %d", len(pl.Jobs))
	}
	return pl.Jobs[0], rep
}

func parseErr(t *testing.T, yml string) {
	t.Helper()
	if _, _, err := drone.New().Parse(repo(map[string]string{".drone.yml": yml})); err == nil {
		t.Fatal("Parse succeeded, want an error")
	}
}

func step(t *testing.T, j *ir.Job, id string) *ir.Step {
	t.Helper()
	for _, s := range j.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q not found", id)
	return nil
}

func ext(e ir.Ext) map[string]any {
	m, _ := e["drone"].(map[string]any)
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

func condText(e expr.Expr) string {
	if e == nil {
		return ""
	}
	return expr.String(e)
}
