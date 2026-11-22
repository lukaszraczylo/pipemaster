package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

func TestCapsLedger(t *testing.T) {
	m := tekton.New().Caps()
	for _, f := range caps.AllFeatures() {
		if _, ok := m[f]; !ok {
			t.Errorf("feature %s is not declared", f)
		}
	}
	want := map[caps.Level][]caps.Feature{
		caps.Native: {
			caps.JobNeeds, caps.JobIf, caps.StepIf, caps.JobContainer, caps.JobTimeout, caps.JobRetry,
			caps.JobAllowFailure, caps.JobMatrix, caps.JobOutputs, caps.JobEnv, caps.PipelineInputs,
			caps.PipelineFinally, caps.StepScript, caps.StepImage, caps.StepEnv, caps.StepContinueOnError,
			caps.StepTimeout, caps.StepWorkdir, caps.StepShell, caps.DisplayName,
			caps.ExprOpaque, caps.ExtPassthrough, caps.ScriptInterpolation,
		},
		caps.Emulated: {
			caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerFilterBranch,
			caps.TriggerFilterTags, caps.TriggerFilterPaths, caps.TriggerSchedule, caps.JobServices,
			caps.JobCache, caps.JobArtifacts, caps.JobNeedsArtifacts, caps.PipelineEnv, caps.StepAction,
			caps.ProjectMultiPipeline, caps.ConfigName, caps.TriggerAPI,
		},
		caps.External: {caps.TriggerManual},
		caps.Lost: {
			caps.JobRunner, caps.JobEnvironment, caps.JobEnvironmentApproval, caps.JobManual,
			caps.PipelineConcurrency, caps.PipelinePermissions, caps.TriggerPipelineDone,
			caps.TriggerResource,
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
	for f, s := range tekton.New().Caps() {
		if s.Level != caps.Native && s.Strategy == "" && s.Caveat == "" {
			t.Errorf("%s is %v with neither strategy nor caveat", f, s.Level)
		}
	}
}

func TestCapsCaveats(t *testing.T) {
	m := tekton.New().Caps()
	for f, sub := range map[caps.Feature]string{
		caps.JobMatrix:          "fail-fast",
		caps.PipelineFinally:    "among themselves",
		caps.TriggerPush:        "GitHub",
		caps.TriggerFilterPaths: "addChangedFiles",
		caps.JobServices:        "localhost",
		caps.JobCache:           "ReadWriteMany",
		caps.ConfigName:         "ci-secrets",
		caps.ExprOpaque:         "enable-cel-in-whenexpression",
	} {
		s := m[f]
		if !strings.Contains(s.Strategy+" "+s.Caveat, sub) {
			t.Errorf("%s text %q lacks %q", f, s.Strategy+" "+s.Caveat, sub)
		}
	}
}
