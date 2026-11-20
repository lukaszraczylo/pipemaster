package gha_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

func TestNoopStepReportNamesTheJob(t *testing.T) {
	p := baseProject(t)
	job(p, "build").Steps = nil
	_, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Entries {
		if e.Feature == caps.StepScript {
			if e.IRPath != "pipeline/ci/job/build" {
				t.Errorf("path = %s, want the job that has no steps", e.IRPath)
			}
			return
		}
	}
	t.Errorf("no step.script entry: %+v", rep.Entries)
}
