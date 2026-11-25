package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
)

func TestCapsLedger(t *testing.T) {
	m := argo.New().Caps()
	want := map[caps.Level][]caps.Feature{
		caps.Native: {
			caps.TriggerSchedule, caps.JobNeeds, caps.JobIf, caps.JobContainer, caps.JobTimeout,
			caps.JobRetry, caps.JobAllowFailure, caps.JobOutputs, caps.JobArtifacts, caps.JobNeedsArtifacts,
			caps.JobEnvironmentApproval, caps.JobManual, caps.PipelineInputs, caps.PipelineFinally,
			caps.StepScript, caps.StepImage, caps.StepEnv, caps.StepWorkdir, caps.DisplayName,
			caps.ExprOpaque, caps.ExtPassthrough, caps.ScriptInterpolation,
		},
		caps.Emulated: {
			caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerFilterBranch,
			caps.TriggerFilterTags, caps.TriggerAPI, caps.JobMatrix, caps.JobServices, caps.JobCache,
			caps.JobEnv, caps.PipelineEnv, caps.PipelineConcurrency, caps.StepIf, caps.StepContinueOnError,
			caps.StepAction, caps.StepShell, caps.ConfigName, caps.ProjectMultiPipeline,
		},
		caps.External: {caps.TriggerManual, caps.JobRunner},
		caps.Lost: {
			caps.TriggerFilterPaths, caps.JobEnvironment, caps.PipelinePermissions,
			caps.StepTimeout, caps.TriggerPipelineDone, caps.TriggerResource,
		},
	}
	seen := map[caps.Feature]bool{}
	for lvl, fs := range want {
		for _, f := range fs {
			if seen[f] {
				t.Errorf("%s listed twice in the test table", f)
			}
			seen[f] = true
			got, ok := m[f]
			if !ok {
				t.Errorf("feature %s is not declared", f)
			}
			if got.Level != lvl {
				t.Errorf("%s = %v, want %v", f, got.Level, lvl)
			}
		}
	}
	for _, f := range caps.AllFeatures() {
		if !seen[f] {
			t.Errorf("feature %s is declared but not listed in the test table", f)
		}
	}
	if len(m) != len(caps.AllFeatures()) {
		t.Errorf("matrix holds %d features, caps.AllFeatures has %d", len(m), len(caps.AllFeatures()))
	}
}

func TestCapsNonNativeExplain(t *testing.T) {
	for f, s := range argo.New().Caps() {
		if s.Level != caps.Native && s.Strategy == "" && s.Caveat == "" {
			t.Errorf("%s is %v with neither strategy nor caveat", f, s.Level)
		}
	}
}

func TestCapsCaveats(t *testing.T) {
	m := argo.New().Caps()
	for f, subs := range map[caps.Feature][]string{
		caps.TriggerPush:            {"GitHub", "EventSource", "Sensor"},
		caps.TriggerPullRequest:     {"same repository", "pipemaster-github-webhook", "token"},
		caps.TriggerFilterBranch:    {"Sensor"},
		caps.JobManual:              {"suspend", "RBAC"},
		caps.JobEnvironmentApproval: {"approver"},
		caps.JobMatrix:              {"withItems", "failFast", "parallelism"},
		caps.JobServices:            {"localhost"},
		caps.JobCache:               {"volumeClaimTemplates"},
		caps.JobArtifacts:           {"repository", "one path per artifact"},
		caps.PipelineConcurrency:    {"mutex", "cancel-in-progress"},
		caps.StepContinueOnError:    {"trap", "exits non-zero"},
		caps.StepIf:                 {"containerSet has no when", "guard", "env"},
		caps.JobIf:                  {"depends", "continueOn"},
		caps.JobOutputs:             {"/workspace/.pipemaster/outputs", "main"},
		caps.StepTimeout:            {"containerSet", "timeout"},
		caps.ConfigName:             {"ci-secrets", "ci-vars"},
		caps.TriggerManual:          {"argo submit --from workflowtemplate/"},
		caps.ExprOpaque:             {"argo dialect", "others are lost"},
		caps.TriggerAPI:             {"Argo file held", "suspended CronWorkflow"},
		caps.JobContainer:           {"every container"},
	} {
		s := m[f]
		text := s.Strategy + " " + s.Caveat
		for _, sub := range subs {
			if !strings.Contains(text, sub) {
				t.Errorf("%s text %q lacks %q", f, text, sub)
			}
		}
	}
}

func TestCapsDoNotPromiseEnum(t *testing.T) {
	s := argo.New().Caps()[caps.PipelineInputs]
	if text := s.Strategy + " " + s.Caveat; strings.Contains(text, "enum") {
		t.Errorf("the emitter writes no enum, but the text says: %q", text)
	}
}
