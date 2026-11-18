package ir_test

import (
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

func TestMarshalRejectsDialectWithParen(t *testing.T) {
	p := one(&ir.Job{ID: "a", If: expr.Opaque{Dialect: "a)b", Text: "x"}})
	if _, err := ir.Marshal(p); err == nil {
		t.Fatal("want error")
	}
}
