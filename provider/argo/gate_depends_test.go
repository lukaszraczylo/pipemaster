package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
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
			if job["depends"] != "b-gate.Succeeded && (a.Failed)" || job["dependencies"] != nil {
				t.Errorf("the job does not wait for the gate and the condition: %v", job)
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

// Argo rejects a DAG that mixes depends and dependencies, so every edge of the DAG uses depends.
func TestGateWithADependsExpressionPassesArgoLint(t *testing.T) {
	linttool.RequireArgo(t)
	path := ".argo/workflows/ci.yaml"
	for name, approval := range map[string]bool{"manual": false, "approval": true} {
		t.Run(name, func(t *testing.T) {
			files, _ := emit(t, failedNeedsGate(approval))
			if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
				t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
			}
		})
	}
}

func TestPlainNeedsJoinADependsDAG(t *testing.T) {
	linttool.RequireArgo(t)
	p := failedNeedsGate(false)
	c := scriptJob("c", "x")
	c.Needs = []ir.Need{{JobID: "a"}}
	p.Pipelines[0].Jobs = append(p.Pipelines[0].Jobs, c)
	files, _ := emit(t, p)
	path := ".argo/workflows/ci.yaml"
	ts := tasks(t, template(t, files))
	if got := named(t, ts, "c"); got["depends"] != "a.Succeeded" || got["dependencies"] != nil {
		t.Errorf("a plain need does not use depends: %v", got)
	}
	if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
	}
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("trip: %s\n%s", diff, joinFiles(files))
	}
}

func TestPlainNeedsKeepDependencies(t *testing.T) {
	a, b := scriptJob("a", "x"), scriptJob("b", "x")
	b.Needs = []ir.Need{{JobID: "a"}}
	files, _ := emit(t, project(a, b))
	got := named(t, tasks(t, template(t, files)), "b")
	if len(list(got["dependencies"])) != 1 || got["depends"] != nil {
		t.Errorf("plain needs changed form: %v", got)
	}
}
