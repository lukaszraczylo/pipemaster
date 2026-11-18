package ir_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func ids(js []*ir.Job) []string {
	var out []string
	for _, j := range js {
		out = append(out, j.ID)
	}
	return out
}

func TestNormalizeSortsJobsKeepsSteps(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{
		{ID: "z"},
		{
			ID: "a",
			Triggers: []ir.Trigger{
				{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main", "dev"}}},
				{Kind: ir.TriggerPR},
			},
			Jobs: []*ir.Job{
				{
					ID:     "b",
					Runner: ir.Runner{Labels: []string{"y", "x"}},
					Needs:  []ir.Need{{JobID: "c"}, {JobID: "a"}},
					Steps:  []*ir.Step{{ID: "s2"}, {ID: "s1"}},
					Env:    []ir.Var{{Name: "B"}, {Name: "A"}},
				},
				{ID: "a"},
			},
			Finally: []*ir.Job{{ID: "g"}, {ID: "f"}},
		},
	}}
	ir.Normalize(p)
	if p.Pipelines[0].ID != "a" || p.Pipelines[1].ID != "z" {
		t.Fatalf("pipelines not sorted: %s %s", p.Pipelines[0].ID, p.Pipelines[1].ID)
	}
	a := p.Pipelines[0]
	if got := ids(a.Jobs); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("jobs %v", got)
	}
	if got := ids(a.Finally); !reflect.DeepEqual(got, []string{"f", "g"}) {
		t.Fatalf("finally %v", got)
	}
	b := a.Jobs[1]
	if b.Steps[0].ID != "s2" || b.Env[0].Name != "B" {
		t.Fatal("steps/env order must be kept")
	}
	if b.Needs[0].JobID != "a" || !reflect.DeepEqual(b.Runner.Labels, []string{"x", "y"}) {
		t.Fatalf("needs/labels not sorted: %+v %v", b.Needs, b.Runner.Labels)
	}
	if a.Triggers[0].Kind != ir.TriggerPR && a.Triggers[0].Kind != ir.TriggerPush {
		t.Fatal("unexpected trigger")
	}
	if a.Triggers[0].Kind != ir.TriggerPR { // "pr" < "push"
		t.Fatalf("triggers not sorted by kind: %v", a.Triggers[0].Kind)
	}
	if got := a.Triggers[1].Branches.Include; !reflect.DeepEqual(got, []string{"main", "dev"}) {
		t.Fatalf("filter order must be kept: %v", got)
	}
}

func TestNormalizeKeepsMatrixAxisOrder(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "a", Jobs: []*ir.Job{{ID: "j", Matrix: &ir.Matrix{
		Axes:    []ir.Axis{{Name: "os", Values: []string{"linux", "mac"}}, {Name: "go", Values: []string{"1.2", "1.1"}}},
		Include: []ir.Combo{{{Key: "z", Value: "1"}, {Key: "a", Value: "2"}}},
	}}}}}}
	ir.Normalize(p)
	m := p.Pipelines[0].Jobs[0].Matrix
	if m.Axes[0].Name != "os" || m.Axes[1].Values[0] != "1.2" {
		t.Fatal("axes must keep order")
	}
	if m.Include[0][0].Key != "a" {
		t.Fatalf("combo not sorted by key: %v", m.Include[0])
	}
}

func TestNormalizeNilSafe(_ *testing.T) {
	ir.Normalize(nil)
	ir.Normalize(&ir.Project{Pipelines: []*ir.Pipeline{nil, {ID: "a", Jobs: []*ir.Job{nil}}}})
}

func TestNormalizeTriggerTieBreakOnExt(t *testing.T) {
	mk := func(ev string) ir.Trigger {
		return ir.Trigger{Kind: ir.TriggerAPI, Ext: ir.Ext{"gha": map[string]any{"event": ev}}}
	}
	a := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Triggers: []ir.Trigger{mk("workflow_call"), mk("repository_dispatch")}}}}
	b := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "p", Triggers: []ir.Trigger{mk("repository_dispatch"), mk("workflow_call")}}}}
	ir.Normalize(a)
	ir.Normalize(b)
	if ok, diff := ir.Equal(a, b); !ok {
		t.Fatalf("trigger order differs after Normalize: %s", diff)
	}
}
