package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestJobAndPipelineEnvWithStoreReferencesComeBack(t *testing.T) {
	secret := ir.Var{Name: "API", Value: ir.Value{Kind: ir.ValueSecret, Secret: "API_KEY"}}
	config := ir.Var{Name: "REGION", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixVars, "REGION"}}}}
	j := scriptJob("build", "echo ok")
	j.Env = []ir.Var{{Name: "GOFLAGS", Value: ir.Value{Lit: "-trimpath"}}, secret, config}
	p := project(j)
	p.Pipelines[0].Env = []ir.Var{{Name: "TOKEN", Value: ir.Value{Kind: ir.ValueSecret, Secret: "TOK"}}}
	files, _ := emit(t, p)
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Pipelines[0]
	if len(got.Env) != 1 || got.Env[0].Name != "TOKEN" {
		t.Errorf("pipeline env = %+v, want TOKEN", got.Env)
	}
	gj := got.Jobs[0]
	if len(gj.Env) != 3 {
		t.Errorf("job env = %+v, want GOFLAGS, API, REGION", gj.Env)
	}
	if len(gj.Steps[0].Env) != 0 {
		t.Errorf("step env = %+v, want none", gj.Steps[0].Env)
	}
	if hasEntry(rep, "marker ignored") {
		t.Errorf("unexpected entry: %v", rep.Entries)
	}
}
