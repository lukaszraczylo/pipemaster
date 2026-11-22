package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestJobWithNoStepReportsThePlaceholderStep(t *testing.T) {
	_, rep := emit(t, project(&ir.Job{ID: "downstream"}))
	for _, e := range rep.Entries {
		if e.IRPath == "pipeline/ci/job/downstream/step/script" && e.Feature == caps.StepScript && e.Level == caps.Lost {
			return
		}
	}
	t.Errorf("no lost step.script entry at the placeholder step: %v", rep.Entries)
}

func TestUnspellableExpressionIsReportedAsScriptInterpolation(t *testing.T) {
	_, rep := emit(t, project(scriptJob("a", "echo start", "echo ${{ hashFiles('**/go.sum') }}")))
	n := 0
	for _, e := range rep.Entries {
		if e.Feature == caps.ScriptInterpolation && e.Level == caps.Lost && e.IRPath == "pipeline/ci/job/a/step/a-run" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d lost script.interpolation entries, want 1: %v", n, rep.Entries)
	}
}
