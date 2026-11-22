package concourse_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
	"github.com/lukaszraczylo/pipemaster/report"
)

func repo(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func parseFiles(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	in := map[string]string{}
	for n, b := range files {
		in[".concourse/"+n] = b
	}
	p, rep, err := concourse.New().Parse(repo(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, rep
}

func parseYAML(t *testing.T, y string) (*ir.Pipeline, *report.Report) {
	t.Helper()
	p, rep := parseFiles(t, map[string]string{"ci.yml": y})
	if len(p.Pipelines) != 1 {
		t.Fatalf("want one pipeline, got %d", len(p.Pipelines))
	}
	return p.Pipelines[0], rep
}

func parseErr(t *testing.T, y string) {
	t.Helper()
	if _, _, err := concourse.New().Parse(repo(map[string]string{".concourse/ci.yml": y})); err == nil {
		t.Fatal("Parse succeeded, want an error")
	}
}

func job(t *testing.T, pl *ir.Pipeline, id string) *ir.Job {
	t.Helper()
	for _, j := range pl.Jobs {
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
	m, _ := e["concourse"].(map[string]any)
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

// gitRepo is a git resource that tracks main.
const gitRepo = "resources:\n- name: repo\n  type: git\n  source: {uri: https://example.com/r.git, branch: main}\n"

// oneJob wraps a plan body in a job named build that triggers on repo.
func oneJob(plan string) string {
	return gitRepo + "jobs:\n- name: build\n  plan:\n  - {get: repo, trigger: true}\n" + plan
}
