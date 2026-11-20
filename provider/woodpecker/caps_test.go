package woodpecker_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

func TestCapsLedger(t *testing.T) {
	m := woodpecker.New().Caps()
	for _, f := range caps.AllFeatures() {
		if _, ok := m[f]; !ok {
			t.Errorf("feature %s is not declared", f)
		}
	}
	want := map[caps.Level][]caps.Feature{
		caps.Native: {
			caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerManual, caps.TriggerFilterBranch,
			caps.TriggerFilterPaths, caps.JobNeeds, caps.JobIf, caps.JobContainer, caps.JobServices, caps.JobMatrix,
			caps.StepScript, caps.StepAction, caps.StepImage, caps.StepIf, caps.StepEnv, caps.StepContinueOnError,
			caps.StepWorkdir, caps.PipelineConcurrency, caps.DisplayName, caps.ConfigName, caps.ExprOpaque,
			caps.ExtPassthrough, caps.ScriptInterpolation,
		},
		caps.Emulated: {
			caps.JobEnv, caps.PipelineEnv, caps.ProjectMultiPipeline, caps.JobCache, caps.JobArtifacts,
			caps.JobAllowFailure, caps.PipelineFinally, caps.JobManual, caps.TriggerFilterTags,
		},
		caps.External: {caps.TriggerSchedule, caps.JobTimeout, caps.JobEnvironmentApproval},
		caps.Lost: {
			caps.JobRetry, caps.JobOutputs, caps.JobRunner, caps.PipelinePermissions, caps.PipelineInputs,
			caps.StepTimeout, caps.StepShell, caps.JobEnvironment, caps.JobNeedsArtifacts, caps.TriggerAPI,
			caps.TriggerPipelineDone, caps.TriggerResource,
		},
	}
	seen := map[caps.Feature]bool{}
	for lvl, fs := range want {
		for _, f := range fs {
			if seen[f] {
				t.Errorf("%s listed twice in the test table", f)
			}
			seen[f] = true
			if got := m[f].Level; got != lvl {
				t.Errorf("%s = %v, want %v", f, got, lvl)
			}
		}
	}
	all := caps.AllFeatures()
	for _, f := range all {
		if !seen[f] {
			t.Errorf("feature %s is declared but not listed in the test table", f)
		}
	}
	if len(seen) != len(all) {
		t.Errorf("the table lists %d features, caps.AllFeatures has %d", len(seen), len(all))
	}
}

func TestCapsNonNativeExplain(t *testing.T) {
	for f, s := range woodpecker.New().Caps() {
		if s.Level != caps.Native && s.Strategy == "" && s.Caveat == "" {
			t.Errorf("%s is %v with neither strategy nor caveat", f, s.Level)
		}
	}
}
