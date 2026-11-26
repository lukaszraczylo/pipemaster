package emitkit_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func sourceProject(origin string, key map[string]any, steps ...*ir.Step) *ir.Project {
	j := &ir.Job{ID: "build", Steps: steps}
	if key != nil {
		j.Ext = ir.Ext{"concourse": map[string]any{emitkit.CheckoutKey: key}}
	}
	return &ir.Project{Origin: ir.Origin{Provider: origin}, Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{j}}}}
}

func TestWithSourceCheckout(t *testing.T) {
	script := &ir.Step{ID: "s", Kind: ir.StepScript, Script: []string{"ls"}}
	tests := []struct {
		p         *ir.Project
		name      string
		wantDepth string
		wantSteps int
		added     bool
	}{
		{name: "adds checkout", p: sourceProject("concourse", map[string]any{}, script), wantSteps: 2, added: true},
		{name: "carries depth", p: sourceProject("concourse", map[string]any{"depth": 1}, script), wantSteps: 2, wantDepth: "1", added: true},
		{name: "no marker", p: sourceProject("concourse", nil, script), wantSteps: 1},
		{name: "other origin", p: sourceProject("argo", map[string]any{}, script), wantSteps: 1},
		{name: "no steps", p: sourceProject("concourse", map[string]any{})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, paths := emitkit.WithSourceCheckout(tc.p)
			steps := got.Pipelines[0].Jobs[0].Steps
			if len(steps) != tc.wantSteps {
				t.Fatalf("steps = %d, want %d", len(steps), tc.wantSteps)
			}
			if tc.added {
				if steps[0].Kind != ir.StepAction || steps[0].Action.Repo != "checkout" || len(paths) != 1 {
					t.Errorf("first step = %+v paths %v", steps[0], paths)
				}
				if d := steps[0].With["fetch-depth"].Lit; d != tc.wantDepth {
					t.Errorf("fetch-depth = %q, want %q", d, tc.wantDepth)
				}
				if len(tc.p.Pipelines[0].Jobs[0].Steps) != 1 {
					t.Error("input project was modified")
				}
			}
		})
	}
}
