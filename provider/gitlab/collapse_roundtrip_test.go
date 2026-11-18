package gitlab_test

import (
	"slices"
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
