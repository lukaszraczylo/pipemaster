package gitlab_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestJobWithNoStepGetsNoPlaceholderStepBack(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "downstream"}}}}}
	ir.Normalize(p)
	files, _ := emitFiles(t, p)
	back, _ := parseFiles(t, map[string]string{".gitlab-ci.yml": string(files[".gitlab-ci.yml"])})
	if got := job(t, back.Pipelines[0], "downstream").Steps; len(got) != 0 {
		t.Fatalf("steps = %+v; want none, because the no-op script only keeps the file valid", got)
	}
}
