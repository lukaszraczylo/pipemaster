package ir_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func one(jobs ...*ir.Job) *ir.Project {
	return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: jobs}}}
}

func TestValidateOK(t *testing.T) {
	p := one(&ir.Job{ID: "build"}, &ir.Job{ID: "deploy", Needs: []ir.Need{{JobID: "build"}}})
	p.Pipelines[0].Finally = []*ir.Job{{ID: "notify", Needs: []ir.Need{{JobID: "deploy"}}}}
	if err := ir.Validate(p); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDuplicateJobID(t *testing.T) {
	err := ir.Validate(one(&ir.Job{ID: "a"}, &ir.Job{ID: "a"}))
	if err == nil || !strings.Contains(err.Error(), "pipeline/ci/job/a") || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateDuplicateAcrossFinally(t *testing.T) {
	p := one(&ir.Job{ID: "a"})
	p.Pipelines[0].Finally = []*ir.Job{{ID: "a"}}
	if err := ir.Validate(p); err == nil {
		t.Fatal("want duplicate error")
	}
}

func TestValidateDuplicatePipelineAndStep(t *testing.T) {
	p := &ir.Project{Pipelines: []*ir.Pipeline{
		{ID: "ci", Jobs: []*ir.Job{{ID: "a", Steps: []*ir.Step{{ID: "s"}, {ID: "s"}}}}},
		{ID: "ci"},
	}}
	err := ir.Validate(p)
	if err == nil {
		t.Fatal("want errors")
	}
	for _, want := range []string{"pipeline/ci: duplicate pipeline id", "pipeline/ci/job/a/step/s: duplicate step id"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}

func TestValidateEmptyJobID(t *testing.T) {
	err := ir.Validate(one(&ir.Job{}))
	if err == nil || !strings.Contains(err.Error(), "empty job id") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateMissingNeed(t *testing.T) {
	err := ir.Validate(one(&ir.Job{ID: "deploy", Needs: []ir.Need{{JobID: "build"}}}))
	if err == nil {
		t.Fatal("want error")
	}
	s := err.Error()
	if !strings.Contains(s, "pipeline/ci/job/deploy") || !strings.Contains(s, `needs unknown job "build"`) {
		t.Fatalf("got %v", s)
	}
	var e *ir.Error
	if !errors.As(err, &e) || e.Path != "pipeline/ci/job/deploy" {
		t.Fatalf("want *ir.Error, got %#v", e)
	}
}

func TestValidateCycle(t *testing.T) {
	err := ir.Validate(one(
		&ir.Job{ID: "a", Needs: []ir.Need{{JobID: "b"}}},
		&ir.Job{ID: "b", Needs: []ir.Need{{JobID: "a"}}},
	))
	if err == nil || !strings.Contains(err.Error(), "needs cycle: a -> b -> a") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSelfCycle(t *testing.T) {
	err := ir.Validate(one(&ir.Job{ID: "a", Needs: []ir.Need{{JobID: "a"}}}))
	if err == nil || !strings.Contains(err.Error(), "needs cycle: a -> a") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateNilSafe(t *testing.T) {
	if err := ir.Validate(nil); err != nil {
		t.Fatal(err)
	}
}
