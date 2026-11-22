package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func failedNeedsGate(approval bool) *ir.Project {
	a, b := scriptJob("a", "x"), scriptJob("b", "x")
	b.Needs = []ir.Need{{JobID: "a"}}
	b.If = expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"needs", "a", "result"}}, R: expr.Lit{Kind: expr.LitStr, Str: "failure"}}
	if approval {
		b.Environment = &ir.Environment{Name: "prod", Approval: true}
	} else {
		b.Manual = true
	}
	return project(a, b)
}

// A gate is a security control: a job with a depends expression still waits for the gate.
func TestGateSurvivesADependsExpression(t *testing.T) {
	for name, approval := range map[string]bool{"manual": false, "approval": true} {
		t.Run(name, func(t *testing.T) {
			files, rep := emit(t, failedNeedsGate(approval))
			ts := tasks(t, template(t, files))
			gate := named(t, ts, "b-gate")
			if gate["depends"] != "a.Failed" {
				t.Errorf("the gate does not carry the condition: %v", gate)
			}
			job := named(t, ts, "b")
			if deps := list(job["dependencies"]); len(deps) != 1 || deps[0] != "b-gate" || job["depends"] != nil {
				t.Errorf("the job does not wait for the gate alone: %v", job)
			}
			if e := entries(rep, "no approval"); len(e) > 0 {
				t.Errorf("the report says the job runs unapproved: %v", e)
			}
		})
	}
}

func TestGateWithADependsExpressionReadsBack(t *testing.T) {
	p := failedNeedsGate(false)
	files, _ := emit(t, p)
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("gate trip: %s\n%s", diff, joinFiles(files))
	}
	if strings.Contains(joinFiles(files), "b-gate") == false {
		t.Error("no gate task written")
	}
}
