package gitlab_test

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
)

const rootFile = ".gitlab-ci.yml"

func repo(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func parseFiles(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	p, rep, err := gitlab.New().Parse(repo(files))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p, rep
}

func parseErr(t *testing.T, files map[string]string) error {
	t.Helper()
	_, _, err := gitlab.New().Parse(repo(files))
	if err == nil {
		t.Fatal("Parse succeeded, want an error")
	}
	return err
}

func pipeline(t *testing.T, yml string) (*ir.Pipeline, *report.Report) {
	t.Helper()
	p, rep := parseFiles(t, map[string]string{rootFile: yml})
	return p.Pipelines[0], rep
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

func needIDs(j *ir.Job) []string {
	var out []string
	for _, n := range j.Needs {
		out = append(out, n.JobID)
	}
	return out
}

func ext(m ir.Ext) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m["gitlab"].(map[string]any)
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

func newProvider() interface {
	Parse(fsys fs.FS) (*ir.Project, *report.Report, error)
} {
	return gitlab.New()
}
