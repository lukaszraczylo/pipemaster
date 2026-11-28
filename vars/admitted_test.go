package vars

import (
	"slices"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func kindList(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestAdmittedKinds(t *testing.T) {
	onBranch := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: expr.Lit{Kind: expr.LitStr, Str: "main"}}
	trig := func(kinds ...ir.TriggerKind) []ir.Trigger {
		var out []ir.Trigger
		for _, k := range kinds {
			out = append(out, ir.Trigger{Kind: k})
		}
		return out
	}
	cases := []struct {
		name string
		pl   *ir.Pipeline
		j    *ir.Job
		want []string
	}{
		{"no trigger runs on every default kind", &ir.Pipeline{}, nil, []string{kindManual, kindPR, kindPush, kindSchedule, kindTag}},
		{"schedule trigger admits schedule", &ir.Pipeline{Triggers: trig(ir.TriggerSchedule)}, nil, []string{kindSchedule}},
		{"push and schedule", &ir.Pipeline{Triggers: trig(ir.TriggerPush, ir.TriggerSchedule)}, nil, []string{kindPush, kindSchedule}},
		{
			// GitLab lets a schedule target a branch or a tag (https://docs.gitlab.com/ci/pipelines/schedules/,
			// "Select the branch or tag for the pipeline"), so a schedule run has a branch only when the target is
			// one. The page does not say whether CI_COMMIT_BRANCH is set. The rule treats a schedule run as
			// branchless, and this case pins that.
			"branch job drops schedule", &ir.Pipeline{Triggers: trig(ir.TriggerPush, ir.TriggerSchedule)},
			&ir.Job{If: onBranch}, []string{kindPush},
		},
		{"branch job keeps pull request", &ir.Pipeline{Triggers: trig(ir.TriggerPR, ir.TriggerSchedule)}, &ir.Job{If: onBranch}, []string{kindPR}},
		{"manual trigger", &ir.Pipeline{Triggers: trig(ir.TriggerManual)}, nil, []string{kindManual}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := kindList(admitted(c.pl, c.j)); !slices.Equal(got, c.want) {
				t.Errorf("admitted = %v, want %v", got, c.want)
			}
		})
	}
}
