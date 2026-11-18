package ir_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func twoJobs(order ...string) *ir.Project {
	var js []*ir.Job
	for _, id := range order {
		js = append(js, &ir.Job{ID: id, Steps: []*ir.Step{{ID: "s1"}, {ID: "s2"}}})
	}
	return one(js...)
}

func TestEqualIgnoresJobOrder(t *testing.T) {
	if ok, d := ir.Equal(twoJobs("a", "b"), twoJobs("b", "a")); !ok {
		t.Fatalf("diff: %s", d)
	}
}

func TestEqualDetectsStepOrder(t *testing.T) {
	a, b := twoJobs("a"), twoJobs("a")
	b.Pipelines[0].Jobs[0].Steps[0], b.Pipelines[0].Jobs[0].Steps[1] = b.Pipelines[0].Jobs[0].Steps[1], b.Pipelines[0].Jobs[0].Steps[0]
	ok, d := ir.Equal(a, b)
	if ok || d == "" {
		t.Fatal("step order change must differ")
	}
}

func TestEqualIncludesExt(t *testing.T) {
	a, b := twoJobs("a"), twoJobs("a")
	a.Pipelines[0].Ext = ir.Ext{"gha": map[string]any{"k": "v"}}
	b.Pipelines[0].Ext = ir.Ext{"gha": map[string]any{"k": "w"}}
	if ok, _ := ir.Equal(a, b); ok {
		t.Fatal("ext difference must be detected")
	}
}

func TestEqualNilEmptyEquivalentAndNoMutation(t *testing.T) {
	a, b := twoJobs("b", "a"), twoJobs("a", "b")
	b.Pipelines[0].Triggers = []ir.Trigger{}
	if ok, d := ir.Equal(a, b); !ok {
		t.Fatalf("nil vs empty slice must be equal: %s", d)
	}
	if a.Pipelines[0].Jobs[0].ID != "b" {
		t.Fatal("Equal must not mutate its inputs")
	}
}

func TestEqualNilProjects(t *testing.T) {
	if ok, _ := ir.Equal(nil, nil); !ok {
		t.Fatal("nil == nil")
	}
	if ok, _ := ir.Equal(nil, &ir.Project{}); ok {
		t.Fatal("nil != non-nil")
	}
}

func TestEqualIgnoresOrigin(t *testing.T) {
	a, b := twoJobs("a"), twoJobs("a")
	a.Origin = ir.Origin{Provider: "gha", Dialect: "gha", Files: []string{"x.yml"}}
	b.Origin = ir.Origin{Provider: "gitlab", Dialect: "gitlab", Files: []string{".gitlab-ci.yml"}}
	if ok, d := ir.Equal(a, b); !ok {
		t.Fatalf("origin must be ignored: %s", d)
	}
	if a.Origin.Provider != "gha" {
		t.Fatal("Equal must not mutate its inputs")
	}
}
