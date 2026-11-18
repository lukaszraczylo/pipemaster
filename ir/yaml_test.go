package ir_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"pgregory.net/rapid"
)

func TestYAMLRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := irtest.GenProject(t, func(caps.Feature) bool { return true })
		b, err := ir.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		q, err := ir.Unmarshal(b)
		if err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		if ok, d := ir.Equal(p, q); !ok {
			t.Fatalf("round trip differs:\n%s\n%s", d, b)
		}
	})
}

func TestYAMLOpaqueAndValues(t *testing.T) {
	to := ir.Duration(90 * time.Second)
	p := one(&ir.Job{
		ID:      "a",
		If:      expr.Opaque{Dialect: "gha", Text: "github.ref == 'refs/heads/main': x\nline2 "},
		Timeout: &to,
		Env:     []ir.Var{{Name: "E", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Opaque{Dialect: "gitlab", Text: ""}}}, {Name: "S", Value: ir.Value{Kind: ir.ValueSecret, Secret: "TOKEN"}}},
		Steps:   []*ir.Step{{ID: "s", If: expr.Opaque{Dialect: "d", Text: "x"}, With: map[string]ir.Value{"k": {Lit: "true"}}}},
	})
	b, err := ir.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "opaque(gha): ") || !strings.Contains(string(b), "1m30s") {
		t.Fatalf("unexpected rendering:\n%s", b)
	}
	q, err := ir.Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if ok, d := ir.Equal(p, q); !ok {
		t.Fatalf("%s\n%s", d, b)
	}
}

func TestYAMLRealExpressions(t *testing.T) {
	cond := expr.Binary{Op: expr.OpAnd,
		L: expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"branch"}}, R: expr.Lit{Kind: expr.LitStr, Str: "it's"}},
		R: expr.Call{Name: "contains", Args: []expr.Expr{expr.Ref{Path: []string{"needs", "build", "outputs", "v"}}, expr.Lit{Kind: expr.LitNum, Num: 1.5}}},
	}
	p := one(&ir.Job{
		ID: "a", If: cond,
		Env:   []ir.Var{{Name: "E", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Unary{Op: expr.OpNot, X: expr.Ref{Path: []string{"matrix", "os"}}}}}},
		Steps: []*ir.Step{{ID: "s", If: expr.Call{Name: "always"}}},
	})
	b, err := ir.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "(branch == 'it''s') && contains(needs.build.outputs.v, 1.5)") {
		t.Fatalf("unexpected rendering:\n%s", b)
	}
	q, err := ir.Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if ok, d := ir.Equal(p, q); !ok {
		t.Fatalf("%s\n%s", d, b)
	}
	for _, u := range ir.Features(q) {
		if u.Feature == caps.ExprOpaque {
			t.Fatal("structured expressions must not report expr.opaque")
		}
	}
}

func TestUnmarshalErrors(t *testing.T) {
	for name, in := range map[string]string{
		"malformed":     "pipelines: [",
		"unknown field": "pipelines:\n- id: a\n  bogus: 1\n",
		"bad expr":      "pipelines:\n- id: a\n  jobs:\n  - id: j\n    if: (unclosed\n",
		"bad duration":  "pipelines:\n- id: a\n  jobs:\n  - id: j\n    timeout: soon\n",
		"bad value":     "pipelines:\n- id: a\n  env:\n  - name: x\n    value: {lit: a, secret: b}\n",
	} {
		if _, err := ir.Unmarshal([]byte(in)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestMarshalOpaqueDialectWithParenRoundTrips(t *testing.T) {
	p := one(&ir.Job{ID: "a", If: expr.Opaque{Dialect: "a)b", Text: "x"}})
	b, err := ir.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	q, err := ir.Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if ok, d := ir.Equal(p, q); !ok {
		t.Fatalf("%s\n%s", d, b)
	}
}

func TestValidateReportsUnmarshalableValues(t *testing.T) {
	for name, v := range map[string]ir.Value{
		"nil expr":     {Kind: ir.ValueExpr},
		"invalid kind": {Kind: 99},
	} {
		p := one(&ir.Job{ID: "a", Env: []ir.Var{{Name: "E", Value: v}}})
		err := ir.Validate(p)
		if err == nil || !strings.Contains(err.Error(), "pipeline/ci/job/a/env/E") {
			t.Errorf("%s: Validate = %v, want error at the value path", name, err)
		}
		if _, err := ir.Marshal(p); err == nil {
			t.Errorf("%s: Marshal must fail too", name)
		}
	}
}

// hostile draws expressions that include ones Check rejects, so the property below is not vacuous.
func hostile(t *rapid.T, label string) expr.Expr {
	switch rapid.IntRange(0, 6).Draw(t, label+"h") {
	case 0:
		return expr.Lit{Kind: expr.LitNum, Num: math.NaN()}
	case 1:
		return expr.Lit{Kind: 9}
	case 2:
		return expr.Call{Name: "not ident"}
	case 3:
		return expr.Opaque{Dialect: "a)b", Text: "x"}
	case 4:
		var e expr.Expr = expr.Ref{Path: []string{"a"}}
		for i := 0; i < rapid.IntRange(1, 70).Draw(t, label+"deep"); i++ {
			e = expr.Unary{Op: expr.OpNot, X: e}
		}
		return e
	case 5:
		return nil
	}
	return irtest.GenExpr(t, label, 4, true)
}

// TestValidatedProjectsMarshalAndRoundTrip: whatever Validate accepts, Marshal prints and Unmarshal reads back.
func TestValidatedProjectsMarshalAndRoundTrip(t *testing.T) {
	accepted, rejected := 0, 0
	rapid.Check(t, func(t *rapid.T) {
		p := irtest.GenProject(t, func(caps.Feature) bool { return true })
		j := p.Pipelines[0].Jobs[0]
		switch rapid.IntRange(0, 3).Draw(t, "slot") {
		case 0:
			j.If = hostile(t, "if")
		case 1:
			j.Steps[0].If = hostile(t, "sif")
		case 2:
			j.Env = append(j.Env, ir.Var{Name: "H", Value: ir.Value{Kind: ir.ValueExpr, Expr: hostile(t, "val")}})
		}
		if ir.Validate(p) != nil {
			rejected++
			return
		}
		accepted++
		b, err := ir.Marshal(p)
		if err != nil {
			t.Fatalf("Validate accepted but Marshal failed: %v", err)
		}
		q, err := ir.Unmarshal(b)
		if err != nil {
			t.Fatalf("%v\n%s", err, b)
		}
		if ok, d := ir.Equal(p, q); !ok {
			t.Fatalf("round trip differs:\n%s", d)
		}
	})
	if accepted == 0 || rejected == 0 {
		t.Fatalf("property is vacuous: accepted=%d rejected=%d", accepted, rejected)
	}
}

func TestMarshalRejectsUnprintableExpressions(t *testing.T) {
	bad := map[string]expr.Expr{
		"nan":       expr.Lit{Kind: expr.LitNum, Num: math.NaN()},
		"inf":       expr.Lit{Kind: expr.LitNum, Num: math.Inf(1)},
		"lit kind":  expr.Lit{Kind: 7},
		"call name": expr.Call{Name: "not an ident"},
	}
	for name, e := range bad {
		if _, err := ir.Marshal(one(&ir.Job{ID: "a", If: e})); err == nil || !strings.Contains(err.Error(), `job "a" if`) {
			t.Errorf("%s: job.if: want error naming the job, got %v", name, err)
		}
		if _, err := ir.Marshal(one(&ir.Job{ID: "a", Steps: []*ir.Step{{ID: "s", If: e}}})); err == nil {
			t.Errorf("%s: step.if: want error", name)
		}
		v := ir.Value{Kind: ir.ValueExpr, Expr: e}
		if _, err := ir.Marshal(one(&ir.Job{ID: "a", Env: []ir.Var{{Name: "E", Value: v}}})); err == nil {
			t.Errorf("%s: value: want error", name)
		}
		p := one(&ir.Job{ID: "a", Steps: []*ir.Step{{ID: "s", With: map[string]ir.Value{"k": v}}}})
		err := ir.Validate(p)
		if err == nil || !strings.Contains(err.Error(), "pipeline/ci/job/a/step/s") {
			t.Errorf("%s: Validate: want error with IR path, got %v", name, err)
		}
	}
}
