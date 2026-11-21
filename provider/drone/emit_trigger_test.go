package drone_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func strs(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, e := range l {
		out = append(out, e.(string))
	}
	return out
}

// triggerOf emits a one-job pipeline with the triggers and returns the trigger block of its document.
func triggerOf(t *testing.T, tr ...ir.Trigger) (map[string]any, string, *report.Report) {
	t.Helper()
	p := gproject(ghaOrigin, gjob("build", gstep("x", "true")))
	p.Pipelines[0].Triggers = tr
	out, rep := emitText(t, p)
	m, _ := docsOf(t, out)[0]["trigger"].(map[string]any)
	return m, out, rep
}

func TestPushBranchesBecomeTheTriggerBranch(t *testing.T) {
	trg, _, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main", "rel-*"}, Exclude: []string{"wip"}}})
	if !slices.Equal(strs(trg["event"]), []string{"push"}) {
		t.Fatalf("event = %v", trg["event"])
	}
	b := trg["branch"].(map[string]any)
	if !slices.Equal(strs(b["include"]), []string{"main", "rel-*"}) || !slices.Equal(strs(b["exclude"]), []string{"wip"}) {
		t.Fatalf("branch = %v", b)
	}
	if _, has := levelOf(rep, caps.TriggerFilterBranch, "trigger/0"); has {
		t.Fatalf("exact globs are native: %v", rep.Entries)
	}
}

func TestInexactGlobIsReported(t *testing.T) {
	_, _, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"release/**"}}})
	wantLevel(t, rep, caps.TriggerFilterBranch, "trigger/0", caps.Emulated)
}

func TestPathsFilterIsNeverWritten(t *testing.T) {
	trg, out, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerPush, Paths: ir.Filter{Include: []string{"src/**"}}})
	if _, has := trg["paths"]; has || strings.Contains(out, "paths") || strings.Contains(out, "src/**") {
		t.Fatalf("a path filter is written, and the server ignores it:\n%s", out)
	}
	wantLevel(t, rep, caps.TriggerFilterPaths, "trigger/0", caps.Lost)
}

func TestPathsInOwnExtIsNeverWritten(t *testing.T) {
	j := gjob("build", gstep("x", "true"))
	j.Ext = ir.Ext{"drone": map[string]any{"trigger": map[string]any{"paths": []any{"a"}, "target": "prod"}}}
	p := gproject("drone", j)
	out := emitUnchecked(t, p)
	trg := docsOf(t, out)[0]["trigger"].(map[string]any)
	if _, has := trg["paths"]; has || trg["target"] != "prod" {
		t.Fatalf("trigger = %v", trg)
	}
}

func TestPushAndPullRequestShareTheBranchFilter(t *testing.T) {
	f := ir.Filter{Include: []string{"main"}}
	trg, _, _ := triggerOf(t, ir.Trigger{Kind: ir.TriggerPush, Branches: f}, ir.Trigger{Kind: ir.TriggerPR, Branches: f})
	if !slices.Equal(strs(trg["event"]), []string{"push", "pull_request"}) || !slices.Equal(strs(trg["branch"]), []string{"main"}) {
		t.Fatalf("trigger = %v", trg)
	}
}

func TestDifferentFiltersBecomeRefs(t *testing.T) {
	trg, out, rep := triggerOf(t,
		ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
		ir.Trigger{Kind: ir.TriggerPR},
		ir.Trigger{Kind: ir.TriggerTag})
	if _, has := trg["branch"]; has {
		t.Fatalf("a branch key would narrow the pull requests: %v", trg)
	}
	want := []string{"refs/heads/main", "refs/tags/**", "refs/pull/*/head"}
	if !slices.Equal(strs(trg["ref"]), want) || !slices.Equal(strs(trg["event"]), []string{"push", "pull_request", "tag"}) {
		t.Fatalf("trigger = %v", trg)
	}
	wantLevel(t, rep, caps.TriggerFilterBranch, "pipeline/drone/trigger/", caps.Emulated)
	if !hasLine(out, "# pipemaster:emulated trigger.filter.branches pipeline/drone") {
		t.Fatalf("no marker:\n%s", out)
	}
}

func TestFilteredPullRequestCannotJoinAnotherFilter(t *testing.T) {
	trg, _, rep := triggerOf(t,
		ir.Trigger{Kind: ir.TriggerPush},
		ir.Trigger{Kind: ir.TriggerPR, Branches: ir.Filter{Include: []string{"main"}}})
	ev, _ := trg["event"].(map[string]any)
	if ex := strs(ev["exclude"]); !slices.Equal(ex, []string{"**"}) {
		t.Fatalf("the document must be disabled: %v", trg)
	}
	wantLevel(t, rep, caps.JobIf, "job/build", caps.Lost)
}

func TestTagFilterIsARef(t *testing.T) {
	trg, out, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}, Exclude: []string{"v0*"}}})
	r := trg["ref"].(map[string]any)
	if !slices.Equal(strs(trg["event"]), []string{"tag"}) || !slices.Equal(strs(r["include"]), []string{"refs/tags/v*"}) || !slices.Equal(strs(r["exclude"]), []string{"refs/tags/v0*"}) {
		t.Fatalf("trigger = %v", trg)
	}
	wantLevel(t, rep, caps.TriggerFilterTags, "trigger/0", caps.Emulated)
	if !hasLine(out, "# pipemaster:emulated trigger.filter.tags pipeline/drone/trigger/0") {
		t.Fatalf("no marker:\n%s", out)
	}
}

func TestTagFilterOnPushIsLost(t *testing.T) {
	_, _, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerPush, Tags: ir.Filter{Include: []string{"v*"}}})
	wantLevel(t, rep, caps.TriggerFilterTags, "trigger/0", caps.Lost)
}

func TestScheduleIsExternalWithTheCommand(t *testing.T) {
	trg, _, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"})
	if !slices.Equal(strs(trg["event"]), []string{"cron"}) || !slices.Equal(strs(trg["cron"]), []string{"drone-schedule-0"}) {
		t.Fatalf("trigger = %v", trg)
	}
	wantLevel(t, rep, caps.TriggerSchedule, "trigger/0", caps.External)
	if !hasEntry(rep, `drone cron add <repo> drone-schedule-0 "0 0 3 * * *"`) {
		t.Fatalf("the report must name the command: %v", rep.Entries)
	}
}

func TestManualTriggerIsCustom(t *testing.T) {
	trg, out, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerManual})
	if !slices.Equal(strs(trg["event"]), []string{"custom"}) {
		t.Fatalf("trigger = %v", trg)
	}
	wantLevel(t, rep, caps.TriggerManual, "trigger/0", caps.Emulated)
	if !hasLine(out, "# pipemaster:emulated trigger.manual pipeline/drone/trigger/0") {
		t.Fatalf("no marker:\n%s", out)
	}
}

func TestTriggerWithNoDroneEventNeverRuns(t *testing.T) {
	trg, _, rep := triggerOf(t, ir.Trigger{Kind: ir.TriggerAPI})
	ev, _ := trg["event"].(map[string]any)
	if !slices.Equal(strs(ev["exclude"]), []string{"**"}) {
		t.Fatalf("trigger = %v", trg)
	}
	wantLevel(t, rep, caps.JobIf, "job/build", caps.Lost)
}

func TestJobConditionJoinsTheTrigger(t *testing.T) {
	p := gproject("drone", gjob("build", gstep("x", "true")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerPR}}
	p.Pipelines[0].Jobs[0].If = eq(ref(expr.CtxEvent), "pull_request")
	out, _ := emitText(t, p)
	trg := docsOf(t, out)[0]["trigger"].(map[string]any)
	if !slices.Equal(strs(trg["event"]), []string{"pull_request"}) {
		t.Fatalf("trigger = %v", trg)
	}
}

func TestEnvironmentApprovalIsPromote(t *testing.T) {
	j := gjob("deploy", gstep("x", "true"))
	j.Environment = &ir.Environment{Name: "prod", URL: "https://x", Approval: true}
	out, rep := emitText(t, gproject(ghaOrigin, j))
	trg := docsOf(t, out)[0]["trigger"].(map[string]any)
	if !slices.Equal(strs(trg["event"]), []string{"promote"}) || !slices.Equal(strs(trg["target"]), []string{"prod"}) {
		t.Fatalf("trigger = %v", trg)
	}
	wantLevel(t, rep, caps.JobEnvironmentApproval, "job/deploy", caps.Emulated)
	wantLevel(t, rep, caps.JobEnvironment, "job/deploy", caps.Lost)
}

func TestManualJobIsCustom(t *testing.T) {
	j := gjob("deploy", gstep("x", "true"))
	j.Manual = true
	out, rep := emitText(t, gproject(ghaOrigin, j))
	trg := docsOf(t, out)[0]["trigger"].(map[string]any)
	if !slices.Equal(strs(trg["event"]), []string{"custom"}) {
		t.Fatalf("trigger = %v", trg)
	}
	wantLevel(t, rep, caps.JobManual, "job/deploy", caps.Emulated)
}

func TestLostPipelineFeatures(t *testing.T) {
	p := gproject(ghaOrigin, gjob("build", gstep("x", "true")))
	pl := p.Pipelines[0]
	pl.Concurrency = &ir.Concurrency{Group: "g", CancelInProgress: true}
	pl.Permissions = &ir.Permissions{Scopes: map[string]string{"contents": "read"}}
	pl.Inputs = []ir.Input{{Name: "i"}}
	pl.Jobs[0].Retry = &ir.Retry{Attempts: 2}
	pl.Jobs[0].Outputs = []ir.Output{{Name: "o", Value: "v"}}
	out, rep := emitText(t, p)
	for _, f := range []caps.Feature{caps.PipelineConcurrency, caps.PipelinePermissions, caps.PipelineInputs} {
		wantLevel(t, rep, f, "pipeline/drone", caps.Lost)
	}
	wantLevel(t, rep, caps.JobRetry, "job/build", caps.Lost)
	wantLevel(t, rep, caps.JobOutputs, "job/build", caps.Lost)
	if strings.Contains(out, "concurrency") {
		t.Fatalf("a lost feature is written:\n%s", out)
	}
}
