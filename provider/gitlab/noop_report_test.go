package gitlab_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestJobWithNoStepReportsThePlaceholderStep(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "downstream"}}}}}
	ir.Normalize(p)
	_, rep := emitFiles(t, p)
	for _, e := range rep.Entries {
		if e.IRPath == "pipeline/ci/job/downstream/step/script" && e.Feature == caps.StepScript && e.Level == caps.Lost {
			return
		}
	}
	t.Errorf("no lost step.script entry at the placeholder step: %v", rep.Entries)
}
