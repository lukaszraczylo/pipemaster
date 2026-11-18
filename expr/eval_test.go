package expr_test

import (
	"math"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
)

type mapEnv struct {
	vars map[string]expr.Value
	st   expr.Status
}

func (m mapEnv) Lookup(path []string) (expr.Value, bool) {
	v, ok := m.vars[join(path)]
	return v, ok
}
func (m mapEnv) Status() expr.Status { return m.st }

func join(p []string) string {
	s := ""
	for i, x := range p {
		if i > 0 {
			s += "."
		}
		s += x
	}
	return s
}

func str(s string) expr.Expr   { return expr.Lit{Kind: expr.LitStr, Str: s} }
func num(f float64) expr.Expr  { return expr.Lit{Kind: expr.LitNum, Num: f} }
func boolean(b bool) expr.Expr { return expr.Lit{Kind: expr.LitBool, Bool: b} }
func null() expr.Expr          { return expr.Lit{} }
func ref(p ...string) expr.Expr {
	return expr.Ref{Path: p}
}
func call(n string, a ...expr.Expr) expr.Expr { return expr.Call{Name: n, Args: a} }
func bin(op string, l, r expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: r} }
func not(x expr.Expr) expr.Expr               { return expr.Unary{Op: expr.OpNot, X: x} }

func testEnv() mapEnv {
	return mapEnv{vars: map[string]expr.Value{
		"branch":    "Main",
		"n":         float64(3),
		"arr":       []any{"a", "B"},
		"obj":       map[string]any{"k": "v"},
		"empty":     "",
		"matrix.os": "linux",
	}}
}

func TestEvalTable(t *testing.T) {
	type tc struct {
		e    expr.Expr
		want expr.Value
		sem  expr.Semantics
		name string
		err  bool
	}
	mk := func(name string, e expr.Expr, sem expr.Semantics, want expr.Value, err bool) tc {
		return tc{e: e, want: want, sem: sem, name: name, err: err}
	}
	cases := []tc{
		mk("lit str", str("x"), expr.Semantics{}, "x", false),
		mk("lit null", null(), expr.Semantics{}, nil, false),
		mk("ref hit", ref("branch"), expr.Semantics{}, "Main", false),
		mk("ref miss is null", ref("nope"), expr.Semantics{}, nil, false),
		mk("not null", not(null()), expr.Semantics{}, true, false),
		mk("not str", not(str("x")), expr.Semantics{}, false, false),
		mk("eq str cs", bin("==", ref("branch"), str("main")), expr.Semantics{}, false, false),
		mk("eq str ci", bin("==", ref("branch"), str("main")), expr.Semantics{CaseInsensitiveEq: true}, true, false),
		mk("ne str ci", bin("!=", ref("branch"), str("main")), expr.Semantics{CaseInsensitiveEq: true}, false, false),
		mk("eq num", bin("==", num(1), num(1)), expr.Semantics{}, true, false),
		mk("eq null null", bin("==", null(), null()), expr.Semantics{}, true, false),
		mk("eq num str coerce", bin("==", num(3), str("3")), expr.Semantics{}, true, false),
		mk("eq num str strict", bin("==", num(3), str("3")), expr.Semantics{StrictTypes: true}, false, false),
		mk("eq null zero coerce", bin("==", null(), num(0)), expr.Semantics{}, true, false),
		mk("eq bool num coerce", bin("==", boolean(true), num(1)), expr.Semantics{}, true, false),
		mk("eq str notnum nan", bin("==", str("abc"), num(0)), expr.Semantics{}, false, false),
		mk("ne nan true", bin("!=", str("abc"), num(0)), expr.Semantics{}, true, false),
		mk("eq empty str num", bin("==", str(""), num(0)), expr.Semantics{}, true, false),
		mk("eq composite never", bin("==", ref("arr"), ref("arr")), expr.Semantics{}, false, false),
		mk("lt num", bin("<", num(1), num(2)), expr.Semantics{}, true, false),
		mk("le num", bin("<=", num(2), num(2)), expr.Semantics{}, true, false),
		mk("gt str", bin(">", str("b"), str("a")), expr.Semantics{}, true, false),
		mk("ge str ci", bin(">=", str("A"), str("b")), expr.Semantics{CaseInsensitiveEq: true}, false, false),
		mk("lt mixed coerce", bin("<", str("2"), num(10)), expr.Semantics{}, true, false),
		mk("lt mixed strict errs", bin("<", str("2"), num(10)), expr.Semantics{StrictTypes: true}, nil, true),
		mk("lt nan false", bin("<", str("x"), num(1)), expr.Semantics{}, false, false),
		mk("and returns left falsy", bin("&&", str(""), str("x")), expr.Semantics{}, "", false),
		mk("and returns right", bin("&&", str("a"), str("x")), expr.Semantics{}, "x", false),
		mk("or returns left truthy", bin("||", str("a"), str("x")), expr.Semantics{}, "a", false),
		mk("or returns right", bin("||", null(), str("x")), expr.Semantics{}, "x", false),
		mk("and short circuits", bin("&&", boolean(false), call("nosuchfn")), expr.Semantics{}, false, false),
		mk("or short circuits", bin("||", boolean(true), call("nosuchfn")), expr.Semantics{}, true, false),
		mk("and does not skip errors", bin("&&", boolean(true), call("nosuchfn")), expr.Semantics{}, nil, true),
		mk("custom truthy", bin("&&", num(0), str("x")), expr.Semantics{Truthy: func(expr.Value) bool { return true }}, "x", false),
		mk("truthy zero", not(num(0)), expr.Semantics{}, true, false),
		mk("truthy nan", not(num(math.NaN())), expr.Semantics{}, true, false),
		mk("truthy array", not(ref("arr")), expr.Semantics{}, false, false),
		mk("match", bin("=~", ref("branch"), str("^Ma")), expr.Semantics{}, true, false),
		mk("nomatch", bin("!~", ref("branch"), str("^Ma")), expr.Semantics{}, false, false),
		mk("match null left", bin("=~", ref("nope"), str("x")), expr.Semantics{}, false, false),
		mk("match bad regex", bin("=~", str("a"), str("(")), expr.Semantics{}, nil, true),
		mk("match non-string", bin("=~", num(1), str("1")), expr.Semantics{}, nil, true),
		mk("unknown op", bin("+", num(1), num(1)), expr.Semantics{}, nil, true),
		mk("unknown unary", expr.Unary{Op: "-", X: num(1)}, expr.Semantics{}, nil, true),
		mk("empty ref path", expr.Ref{}, expr.Semantics{}, nil, true),

		mk("contains str", call("contains", str("Hello"), str("ell")), expr.Semantics{}, true, false),
		mk("contains str case", call("contains", str("Hello"), str("ELL")), expr.Semantics{}, false, false),
		mk("contains str ci", call("contains", str("Hello"), str("ELL")), expr.Semantics{CaseInsensitiveEq: true}, true, false),
		mk("contains arr", call("contains", ref("arr"), str("a")), expr.Semantics{}, true, false),
		mk("contains arr miss", call("contains", ref("arr"), str("z")), expr.Semantics{}, false, false),
		mk("contains arr ci", call("contains", ref("arr"), str("b")), expr.Semantics{CaseInsensitiveEq: true}, true, false),
		mk("contains other", call("contains", num(1), str("1")), expr.Semantics{}, true, false),
		mk("contains null", call("contains", null(), str("1")), expr.Semantics{}, false, false),
		mk("startsWith", call("startsWith", str("Hello"), str("He")), expr.Semantics{}, true, false),
		mk("startsWith ci", call("startsWith", str("Hello"), str("he")), expr.Semantics{CaseInsensitiveEq: true}, true, false),
		mk("startsWith case", call("startsWith", str("Hello"), str("he")), expr.Semantics{}, false, false),
		mk("endsWith", call("endsWith", str("Hello"), str("lo")), expr.Semantics{}, true, false),
		mk("endsWith miss", call("endsWith", str("Hello"), str("He")), expr.Semantics{}, false, false),
		mk("format", call("format", str("{0}-{1}-{0}"), str("a"), num(2)), expr.Semantics{}, "a-2-a", false),
		mk("format braces", call("format", str("{{x}} {0}"), boolean(true)), expr.Semantics{}, "{x} true", false),
		mk("format null", call("format", str("[{0}]"), null()), expr.Semantics{}, "[]", false),
		mk("format oob", call("format", str("{1}"), str("a")), expr.Semantics{}, nil, true),
		mk("format malformed", call("format", str("{x}"), str("a")), expr.Semantics{}, nil, true),
		mk("format unclosed", call("format", str("{0"), str("a")), expr.Semantics{}, nil, true),
		mk("join default", call("join", ref("arr")), expr.Semantics{}, "a,B", false),
		mk("join sep", call("join", ref("arr"), str(" | ")), expr.Semantics{}, "a | B", false),
		mk("join str", call("join", str("x")), expr.Semantics{}, "x", false),
		mk("join null", call("join", null()), expr.Semantics{}, "", false),
		mk("toJSON", call("toJSON", ref("obj")), expr.Semantics{}, `{"k":"v"}`, false),
		mk("toJSON null", call("toJSON", null()), expr.Semantics{}, "null", false),
		mk("fromJSON num", call("fromJSON", str("[1,2]")), expr.Semantics{}, []any{float64(1), float64(2)}, false),
		mk("fromJSON bad", call("fromJSON", str("{")), expr.Semantics{}, nil, true),
		mk("success", call("success"), expr.Semantics{}, true, false),
		mk("failure", call("failure"), expr.Semantics{}, false, false),
		mk("cancelled", call("cancelled"), expr.Semantics{}, false, false),
		mk("always", call("always"), expr.Semantics{}, true, false),
		mk("unknown func", call("nosuchfn"), expr.Semantics{}, nil, true),
		mk("arity startsWith", call("startsWith", str("a")), expr.Semantics{}, nil, true),
		mk("arity success", call("success", str("a")), expr.Semantics{}, nil, true),
		mk("arity format none", call("format"), expr.Semantics{}, nil, true),
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := expr.Eval(c.e, testEnv(), c.sem)
			if c.err {
				if err == nil {
					t.Fatalf("want error, got %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !valueEq(got, c.want) {
				t.Fatalf("got %#v want %#v", got, c.want)
			}
		})
	}
}

func valueEq(a, b expr.Value) bool {
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if aok || bok {
		if !aok || !bok || len(as) != len(bs) {
			return false
		}
		for i := range as {
			if !valueEq(as[i], bs[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

func TestStatusFuncs(t *testing.T) {
	cases := []struct {
		fn   string
		st   expr.Status
		want bool
	}{
		{"success", expr.Success, true}, {"success", expr.Failure, false}, {"success", expr.Cancelled, false},
		{"failure", expr.Success, false}, {"failure", expr.Failure, true}, {"failure", expr.Cancelled, false},
		{"cancelled", expr.Success, false}, {"cancelled", expr.Failure, false}, {"cancelled", expr.Cancelled, true},
		{"always", expr.Success, true}, {"always", expr.Failure, true}, {"always", expr.Cancelled, true},
	}
	for _, c := range cases {
		got, err := expr.Eval(call(c.fn), mapEnv{st: c.st}, expr.Semantics{})
		if err != nil || got != c.want {
			t.Fatalf("%s under %v: got %v, %v want %v", c.fn, c.st, got, err, c.want)
		}
	}
}

func TestOpaqueEvalErrors(t *testing.T) {
	if _, err := expr.Eval(expr.Opaque{Dialect: "gha", Text: "hashFiles('x')"}, testEnv(), expr.Semantics{}); err == nil {
		t.Fatal("opaque must not evaluate")
	}
	if _, err := expr.Eval(bin("&&", boolean(true), expr.Opaque{Dialect: "gha", Text: "x"}), testEnv(), expr.Semantics{}); err == nil {
		t.Fatal("nested opaque must not evaluate")
	}
}

func TestEvalRefNeedsEnv(t *testing.T) {
	if _, err := expr.Eval(ref("a"), nil, expr.Semantics{}); err == nil {
		t.Fatal("nil env must error on ref")
	}
	if _, err := expr.Eval(num(1), nil, expr.Semantics{}); err != nil {
		t.Fatal(err)
	}
}

func TestEvalRejectsUnknownValueType(t *testing.T) {
	env := mapEnv{vars: map[string]expr.Value{"x": struct{}{}}}
	if _, err := expr.Eval(bin("==", ref("x"), num(1)), env, expr.Semantics{}); err == nil {
		t.Fatal("unsupported value type must error")
	}
}
