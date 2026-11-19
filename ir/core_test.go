package ir_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const coreJSON = `{"id":"ci","name":"P","push":{"include":["x"],"set":true},"tag":{"include":[],"set":false},"pr":{"include":[],"set":true},` +
	`"jobs":[{"id":"a","name":"","needs":[],"env":[{"name":"K","value":"v"}],"runner":["r1"],"steps":[{"id":"s1","name":"n","script":["echo 1"],"env":[]}],` +
	`"retry":2,"timeout_min":5,"manual":true,"allow_failure":true,"on_failure":false}],` +
	`"finally":[{"id":"f","name":"","needs":[{"job":"a","artifacts":true}],"env":[],"runner":["r1"],"steps":[],"retry":0,"timeout_min":0,"manual":false,"allow_failure":false,"on_failure":true}]}`

func TestCoreRoundTrip(t *testing.T) {
	p, err := ir.FromCore([]byte(coreJSON))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ir.ToCore(p)
	if err != nil {
		t.Fatal(err)
	}
	var want, have any
	if err := json.Unmarshal([]byte(coreJSON), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(want, have); d != "" {
		t.Errorf("ToCore(FromCore(b)) differs from b (-want +got):\n%s", d)
	}
}

func TestCoreRejectsOutsideSubset(t *testing.T) {
	tests := []struct {
		mutate func(*ir.Project)
		name   string
	}{
		{name: "matrix", mutate: func(p *ir.Project) { p.Pipelines[0].Jobs[0].Matrix = &ir.Matrix{} }},
		{name: "pipeline env", mutate: func(p *ir.Project) {
			p.Pipelines[0].Env = []ir.Var{{Name: "A", Value: ir.Value{Lit: "1"}}}
		}},
		{name: "expression env", mutate: func(p *ir.Project) {
			p.Pipelines[0].Jobs[0].Env = []ir.Var{{Name: "A", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Lit{Kind: expr.LitStr, Str: "x"}}}}
		}},
		{name: "optional need", mutate: func(p *ir.Project) {
			p.Pipelines[0].Finally[0].Needs = []ir.Need{{JobID: "a", Optional: true}}
		}},
		{name: "retry condition", mutate: func(p *ir.Project) { p.Pipelines[0].Jobs[0].Retry.On = []string{"x"} }},
		{name: "fractional timeout", mutate: func(p *ir.Project) {
			d := ir.Duration(90 * time.Second)
			p.Pipelines[0].Jobs[0].Timeout = &d
		}},
		{name: "job condition", mutate: func(p *ir.Project) { p.Pipelines[0].Jobs[0].If = expr.Call{Name: "failure"} }},
		{name: "second pipeline", mutate: func(p *ir.Project) { p.Pipelines = append(p.Pipelines, &ir.Pipeline{ID: "b"}) }},
		{name: "step action", mutate: func(p *ir.Project) { p.Pipelines[0].Jobs[0].Steps[0].Action = &ir.ActionRef{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ir.FromCore([]byte(coreJSON))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(p)
			if _, err := ir.ToCore(p); !errors.Is(err, ir.ErrOutsideCore) {
				t.Errorf("error = %v, want ErrOutsideCore", err)
			}
		})
	}
}

func TestCoreRejectsBadJSON(t *testing.T) {
	if _, err := ir.FromCore([]byte("{")); err == nil {
		t.Error("want a decode error")
	}
}
