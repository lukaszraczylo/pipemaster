package gitlab_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

func reparse(t *testing.T, p *ir.Project) *ir.Project {
	t.Helper()
	files, _ := emitFiles(t, p)
	back, _, err := gitlab.New().Parse(asFS(files))
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, dumpFiles(files))
	}
	return back
}

func TestTriggersThatRulesCannotReadBackSurviveMarkers(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{
		ID: "gitlab-ci",
		Triggers: []ir.Trigger{
			{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"release/*"}, Exclude: []string{"release/old-*"}}},
			{Kind: ir.TriggerPipelineDone},
		},
		Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"make"}}}}},
	}}}
	got := reparse(t, p).Pipelines[0].Triggers
	var push *ir.Trigger
	kinds := map[ir.TriggerKind]bool{}
	for i := range got {
		kinds[got[i].Kind] = true
		if got[i].Kind == ir.TriggerPush {
			push = &got[i]
		}
	}
	if len(got) != 2 || !kinds[ir.TriggerPipelineDone] || push == nil ||
		!slices.Equal(push.Branches.Exclude, []string{"release/old-*"}) || !slices.Equal(push.Branches.Include, []string{"release/*"}) {
		t.Errorf("triggers = %+v", got)
	}
}

func TestStepBoundariesSurviveMarkers(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{
		ID:       "gitlab-ci",
		Triggers: []ir.Trigger{{Kind: ir.TriggerPush}},
		Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{
			{ID: "one", Name: "one", Kind: ir.StepScript, Script: []string{"echo a", "echo b"}},
			{ID: "two", Kind: ir.StepScript, Script: []string{"echo c"}},
		}}},
	}}}
	got := reparse(t, p).Pipelines[0].Jobs[0].Steps
	if len(got) != 2 || got[0].ID != "one" || len(got[0].Script) != 2 || got[1].ID != "two" {
		t.Errorf("steps = %+v", got)
	}
}

func TestEditedWorkflowRulesBeatTriggerMarkers(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{
		ID: "gitlab-ci",
		Triggers: []ir.Trigger{
			{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"release/*"}, Exclude: []string{"release/old-*"}}},
		},
		Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"make"}}}}},
	}}}
	files, _ := emitFiles(t, p)
	text := string(files[rootFile])
	start := strings.Index(text, "workflow:\n")
	end := strings.Index(text, "build:\n")
	if start < 0 || end < start {
		t.Fatalf("unexpected layout:\n%s", text)
	}
	edited := text[:start] + "workflow:\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"schedule\"\n" + text[end:]
	files[rootFile] = []byte(edited)
	back, rep, err := gitlab.New().Parse(asFS(files))
	if err != nil {
		t.Fatal(err)
	}
	got := back.Pipelines[0].Triggers
	if len(got) != 1 || got[0].Kind != ir.TriggerSchedule {
		t.Errorf("the edited rules must win over the marker: %+v", got)
	}
	noted := false
	for _, e := range rep.Entries {
		noted = noted || strings.Contains(e.Detail, "marker ignored")
	}
	if !noted {
		t.Errorf("no report note for the ignored marker: %+v", rep.Entries)
	}
}
