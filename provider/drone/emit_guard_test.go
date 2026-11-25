package drone_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// TestEveryStepOfAChangesJobIsGuarded: a job whose condition is a path predicate gets a shell guard on each step. A
// step that cannot carry one (a plugin step has no shell) must not run unguarded, so the document is disabled;
// an action or pipeline-call step is either not written or written as a guarded script step.
func TestEveryStepOfAChangesJobIsGuarded(t *testing.T) {
	action := func(owner, repo, ref string) *ir.Step {
		return &ir.Step{ID: repo, Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: owner, Repo: repo, Ref: ref}}
	}
	cases := []struct {
		name  string
		steps []*ir.Step
		// disabled: the document must never run, because a step has no place for a guard.
		disabled bool
	}{
		{"script", []*ir.Step{gstep("a", "echo a")}, false},
		{"unknown action", []*ir.Step{gstep("a", "echo a"), action("acme", "deploy", "v1")}, false},
		{"pipeline call", []*ir.Step{gstep("a", "echo a"), {ID: "call", Kind: ir.StepPipelineCall}}, false},
		{"plugin step", []*ir.Step{gstep("a", "echo a"), {ID: "p", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/slack"}}}, true},
		{"plugin step with a script", []*ir.Step{{ID: "p", Kind: ir.StepPlugin, Script: []string{"echo p"}, Image: &ir.Image{Ref: "plugins/slack"}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := gjob("build", c.steps...)
			j.If = expr.Changes("src/**")
			out, _ := emitText(t, gproject("gitlab", j))
			for _, d := range docsOf(t, out) {
				steps, _ := d["steps"].([]any)
				for _, s := range steps {
					st := s.(map[string]any)
					name, _ := st["name"].(string)
					if name == "pipemaster-changes" {
						continue
					}
					cmds, _ := st["commands"].([]any)
					first := ""
					if len(cmds) > 0 {
						first, _ = cmds[0].(string)
					}
					if !strings.Contains(first, "pipemaster:guard") && !c.disabled {
						t.Errorf("step %s runs unguarded:\n%s", name, out)
					}
				}
				if c.disabled && !strings.Contains(out, "- '**'") {
					t.Errorf("a step without a guard slot must disable the document:\n%s", out)
				}
			}
		})
	}
}
