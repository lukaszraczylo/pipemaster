package concourse_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func splitProject(j *ir.Job) *ir.Project {
	p := project(j)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main"), {Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}}}
	return p
}

func planText(t *testing.T, files map[string]string, job string) string {
	t.Helper()
	return fmt.Sprint(jobDoc(t, pipelineDoc(t, files), job)["plan"])
}

func triggers(t *testing.T, files map[string]string, job string) bool {
	t.Helper()
	for _, s := range list(jobDoc(t, pipelineDoc(t, files), job)["plan"]) {
		if dict(s)["trigger"] == true {
			return true
		}
	}
	return false
}

func orCond(l, r expr.Expr) expr.Expr { return expr.Binary{Op: expr.OpOr, L: l, R: r} }

func TestStepConditionOnEventFoldsPerTwin(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = orCond(eqRef("event", "tag"), eqRef("branch", "main"))
	p := splitProject(j)
	files, rep := emit(t, p)
	if push := planText(t, files, "build"); !strings.Contains(push, "PM_CTX_BRANCH") || strings.Contains(push, "exit 0 # pipemaster:guard") {
		t.Errorf("the push job must guard on the branch only: %s", push)
	}
	if tag := planText(t, files, "build-tag"); strings.Contains(tag, "PM_CTX") || strings.Contains(tag, "pipemaster:guard") {
		t.Errorf("the tag twin must hold no guard: %s", tag)
	}
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && e.Feature == caps.StepIf {
			t.Errorf("the condition is reported lost: %+v", e)
		}
	}
	back, _ := reparse(t, files)
	if got := cond(back.Pipelines[0].Jobs[0].Steps[0].If); got != cond(j.Steps[0].If) {
		t.Errorf("the condition came back as %q, want %q", got, cond(j.Steps[0].If))
	}
}

func TestJobConditionOnRefFoldsPerTwin(t *testing.T) {
	for _, tc := range []struct {
		cond        expr.Expr
		name        string
		pushTrigger bool
		tagTrigger  bool
		pushGuard   bool
	}{
		{eqRef("ref", "refs/heads/main"), "branch ref", true, false, true},
		{expr.Call{Name: "startsWith", Args: []expr.Expr{expr.Ref{Path: []string{"ref"}}, expr.Lit{Kind: expr.LitStr, Str: "refs/tags/"}}}, "tag prefix", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := scriptJob("build", "make")
			j.If = tc.cond
			files, rep := emit(t, splitProject(j))
			if got := triggers(t, files, "build"); got != tc.pushTrigger {
				t.Errorf("push job triggers = %v, want %v", got, tc.pushTrigger)
			}
			if got := triggers(t, files, "build-tag"); got != tc.tagTrigger {
				t.Errorf("tag twin triggers = %v, want %v", got, tc.tagTrigger)
			}
			if got := strings.Contains(planText(t, files, "build"), "PM_CTX_BRANCH"); got != tc.pushGuard {
				t.Errorf("push job guards on the branch = %v, want %v", got, tc.pushGuard)
			}
			for _, e := range rep.Entries {
				if e.Level == caps.Lost && e.Feature == caps.JobIf {
					t.Errorf("the condition is reported lost: %+v", e)
				}
			}
			back, _ := reparse(t, files)
			if got := cond(back.Pipelines[0].Jobs[0].If); got != cond(tc.cond) {
				t.Errorf("the condition came back as %q, want %q", got, cond(tc.cond))
			}
		})
	}
}

func TestTagConditionReadsTheTagFileOfTheTwin(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = eqRef("tag", "v1")
	files, _ := emit(t, splitProject(j))
	if tag := planText(t, files, "build-tag"); !strings.Contains(tag, ".git/tags") {
		t.Errorf("the tag twin must read .git/tags: %s", tag)
	}
	if push := planText(t, files, "build"); strings.Contains(push, ".git/tags") || !strings.Contains(push, "exit 0 # pipemaster:guard") {
		t.Errorf("the push job has no tag: %s", push)
	}
}

func TestConditionStillUnknownAfterFoldingIsLost(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = orCond(eqRef("event", "tag"), eqRef("actor", "octocat"))
	files, rep := emit(t, splitProject(j))
	if tag := planText(t, files, "build-tag"); strings.Contains(tag, "exit 0 # pipemaster:guard") {
		t.Errorf("the tag twin holds the condition true: %s", tag)
	}
	if push := planText(t, files, "build"); !strings.Contains(push, "exit 0 # pipemaster:guard") {
		t.Errorf("the push job must fail safe: %s", push)
	}
	if !hasFeatureEntry(rep, string(caps.StepIf), "actor") {
		t.Errorf("no lost step.if entry naming the reference: %+v", rep.Entries)
	}
}
