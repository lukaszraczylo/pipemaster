package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

func TestCapsLedger(t *testing.T) {
	m := concourse.New().Caps()
	want := map[caps.Level][]caps.Feature{
		caps.Native: {
			caps.TriggerPush, caps.TriggerFilterBranch, caps.TriggerFilterPaths, caps.TriggerManual,
			caps.JobNeeds, caps.JobContainer, caps.StepScript, caps.StepImage, caps.StepEnv,
			caps.StepContinueOnError, caps.StepTimeout, caps.StepWorkdir, caps.ConfigName, caps.DisplayName,
			caps.ExtPassthrough, caps.ScriptInterpolation, caps.JobCache,
		},
		caps.Emulated: {
			caps.TriggerTag, caps.TriggerFilterTags, caps.TriggerSchedule, caps.JobMatrix, caps.JobIf, caps.StepIf,
			caps.JobTimeout, caps.JobRetry, caps.JobAllowFailure, caps.JobEnvironmentApproval, caps.JobManual,
			caps.JobArtifacts, caps.PipelineFinally, caps.PipelineConcurrency, caps.JobEnv,
			caps.PipelineEnv, caps.StepShell, caps.StepAction, caps.JobRunner, caps.ProjectMultiPipeline,
		},
		caps.External: {caps.JobNeedsArtifacts},
		caps.Lost: {
			caps.TriggerPullRequest, caps.TriggerAPI, caps.TriggerPipelineDone, caps.TriggerResource,
			caps.PipelineInputs, caps.PipelinePermissions, caps.JobOutputs, caps.JobServices,
			caps.JobEnvironment, caps.ExprOpaque,
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
	for f, s := range concourse.New().Caps() {
		if s.Level != caps.Native && s.Strategy == "" && s.Caveat == "" {
			t.Errorf("%s is %v with neither strategy nor caveat", f, s.Level)
		}
	}
}

func TestCapsCaveats(t *testing.T) {
	m := concourse.New().Caps()
	for f, subs := range map[caps.Feature][]string{
		caps.TriggerPush:            {"git resource", "trigger: true"},
		caps.TriggerFilterBranch:    {"one exact branch", "glob is lost"},
		caps.TriggerFilterPaths:     {"paths", "ignore_paths"},
		caps.TriggerTag:             {"tag_filter"},
		caps.TriggerSchedule:        {"time resource", "interval", "start", "days", "lost", "cron text", "no clock alignment", "first check emits a version at once", "check cadence", "within 5 minutes after the cron time", "passes midnight"},
		caps.TriggerAPI:             {"no resource", "Ext", "lost"},
		caps.TriggerPullRequest:     {"pull request resource type"},
		caps.JobManual:              {"no triggering get", "trigger set", "Ext"},
		caps.JobEnvironmentApproval: {"approver", "trigger set"},
		caps.JobMatrix:              {"across", "not visible outside"},
		caps.JobAllowFailure:        {"try", "success"},
		caps.JobNeeds:               {"passed"},
		caps.JobNeedsArtifacts:      {"s3", "registry"},
		caps.JobArtifacts:           {"workspace"},
		caps.PipelineConcurrency:    {"serial_groups", "cancel-in-progress"},
		caps.PipelineFinally:        {"hook", "single sink"},
		caps.JobRunner:              {"tags", "workers"},
		caps.ConfigName:             {"((name))", "credential manager"},
		caps.JobCache:               {"caches"},
		caps.ExprOpaque:             {"no expression language"},
		caps.StepTimeout:            {"timeout"},
		caps.StepWorkdir:            {"run.dir"},
		caps.JobContainer:           {"image_resource"},
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
