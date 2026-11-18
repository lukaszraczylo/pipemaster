package expr_test

import (
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"pgregory.net/rapid"
)

func TestCheckRejects(t *testing.T) {
	good := ref("a")
	for name, e := range map[string]expr.Expr{
		"nil":               nil,
		"nan":               expr.Lit{Kind: expr.LitNum, Num: math.NaN()},
		"inf":               expr.Lit{Kind: expr.LitNum, Num: math.Inf(1)},
		"-inf":              expr.Lit{Kind: expr.LitNum, Num: math.Inf(-1)},
		"unknown lit kind":  expr.Lit{Kind: 9},
		"stray num":         expr.Lit{Kind: expr.LitStr, Str: "x", Num: 1},
		"stray str":         expr.Lit{Kind: expr.LitNum, Str: "x"},
		"stray bool":        expr.Lit{Kind: expr.LitNull, Bool: true},
		"call bad name":     expr.Call{Name: "a b"},
		"call empty name":   expr.Call{},
		"call digit name":   expr.Call{Name: "1f"},
		"empty ref":         expr.Ref{},
		"unary bad op":      expr.Unary{Op: "-", X: good},
		"unary nil operand": expr.Unary{Op: expr.OpNot},
		"binary bad op":     expr.Binary{Op: "+", L: good, R: good},
		"binary nil left":   expr.Binary{Op: expr.OpEq, R: good},
		"nested nan":        bin("&&", good, call("f", expr.Lit{Kind: expr.LitNum, Num: math.NaN()})),
		"call nil arg":      expr.Call{Name: "f", Args: []expr.Expr{nil}},
	} {
		if err := expr.Check(e); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestCheckAccepts(t *testing.T) {
	for _, e := range []expr.Expr{
		ref("a"), ref("needs", "build linux", "x'y"), ref(""), str("it's"), null(), boolean(true), num(-0.5),
		call("f"), call("true"), bin("=~", ref("a"), str("x")), not(ref("a")), expr.Opaque{Dialect: "a)b", Text: "x"},
		bin("&&", ref("a"), expr.Opaque{}),
	} {
		if err := expr.Check(e); err != nil {
			t.Errorf("%#v: %v", e, err)
		}
	}
}

func TestParseDepthCap(t *testing.T) {
	deep := strings.Repeat("(", 100000) + "a" + strings.Repeat(")", 100000)
	_, err := expr.ParseCanonical(deep)
	if err == nil || !strings.Contains(err.Error(), "deeper than") {
		t.Fatalf("want depth error, got %v", err)
	}
	if _, err := expr.ParseCanonical(strings.Repeat("!", 100000) + "a"); err == nil {
		t.Fatal("deep unary must error")
	}
	if _, err := expr.ParseCanonical(strings.Repeat("f(", 100000) + "a" + strings.Repeat(")", 100000)); err == nil {
		t.Fatal("deep calls must error")
	}
	ok := strings.Repeat("(", 20) + "a" + strings.Repeat(")", 20)
	if _, err := expr.ParseCanonical(ok); err != nil {
		t.Fatalf("moderate nesting must parse: %v", err)
	}
}

func TestCheckValidRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := irtest.GenExpr(t, "e", 4, true)
		if err := expr.Check(e); err != nil {
			t.Fatalf("generated expression fails Check: %v", err)
		}
		back, err := expr.ParseCanonical(expr.String(e))
		if err != nil {
			t.Fatal(err)
		}
		if d := cmp.Diff(e, back, cmpopts.EquateEmpty()); d != "" {
			t.Fatal(d)
		}
	})
}

// nest wraps the leaf n times.
func nest(n int, leaf expr.Expr, wrap func(expr.Expr) expr.Expr) expr.Expr {
	e := leaf
	for i := 0; i < n; i++ {
		e = wrap(e)
	}
	return e
}

// Nesting is one unit per paren, '!' or call level; a Binary operand that is itself Binary prints in parens.
func TestDepthBoundaryAgreesBetweenCheckAndParse(t *testing.T) {
	a := ref("a")
	binLeft := func(e expr.Expr) expr.Expr { return bin("==", e, a) }
	notBin := func(e expr.Expr) expr.Expr { return not(bin("==", e, a)) }
	m := expr.MaxDepth
	cases := []struct {
		e    expr.Expr
		name string
		ok   bool
	}{
		{nest(m+1, a, binLeft), "binary at max", true},
		{nest(m+2, a, binLeft), "binary over", false},
		{nest(m, a, not), "unary at max", true},
		{nest(m+1, a, not), "unary over", false},
		{nest(m, a, func(e expr.Expr) expr.Expr { return call("f", e) }), "call at max", true},
		{nest(m+1, a, func(e expr.Expr) expr.Expr { return call("f", e) }), "call over", false},
		{nest(m/2-1, not(bin("==", a, a)), notBin), "unary of binary at max", true},
		{nest(m/2, not(bin("==", a, a)), notBin), "unary of binary over", false},
	}
	for _, c := range cases {
		cerr := expr.Check(c.e)
		_, perr := expr.ParseCanonical(expr.String(c.e))
		if (cerr == nil) != c.ok || (perr == nil) != c.ok {
			t.Errorf("%s: Check err=%v, ParseCanonical err=%v, want ok=%v", c.name, cerr, perr, c.ok)
		}
	}
}

func TestOpaqueNamedCallIsReserved(t *testing.T) {
	c := call("opaque", str("x): y"))
	if err := expr.Check(c); err == nil {
		t.Fatalf("Check must reject a call named opaque; it prints as %q", expr.String(c))
	}
	for _, in := range []string{"opaque(1)", "a && opaque('x')", "f(opaque())"} {
		if e, err := expr.ParseCanonical(in); err == nil {
			t.Errorf("%q: want error, got %#v", in, e)
		}
	}
	got, err := expr.ParseCanonical("opaque(gha): x")
	if err != nil || got != (expr.Opaque{Dialect: "gha", Text: "x"}) {
		t.Fatalf("top-level opaque form must still parse: %#v, %v", got, err)
	}
}

// TestCheckValidAlwaysRoundTrips covers adversarial shapes, so rejection is the only way out for unprintable ones.
func TestCheckValidAlwaysRoundTrips(t *testing.T) {
	valid, invalid := 0, 0
	rapid.Check(t, func(t *rapid.T) {
		e := irtest.GenExprAdversarial(t, "e", 4, true)
		if expr.Check(e) != nil {
			invalid++
			return
		}
		valid++
		s := expr.String(e)
		back, err := expr.ParseCanonical(s)
		if err != nil {
			t.Fatalf("ParseCanonical(%q): %v", s, err)
		}
		if d := cmp.Diff(e, back, cmpopts.EquateEmpty()); d != "" {
			t.Fatalf("%q: %s", s, d)
		}
	})
	if valid == 0 || invalid == 0 {
		t.Fatalf("vacuous: valid=%d invalid=%d", valid, invalid)
	}
}

func TestFlatChainOfAssociativeOperator(t *testing.T) {
	for _, op := range []string{expr.OpAnd, expr.OpOr} {
		e := ref("a")
		for i := 0; i < 4*expr.MaxDepth; i++ {
			e = bin(op, e, ref("a"))
		}
		s := expr.String(e)
		if strings.Contains(s, "(") {
			t.Fatalf("%s chain must print flat, got %.40q", op, s)
		}
		if err := expr.Check(e); err != nil {
			t.Fatalf("%s chain: %v", op, err)
		}
		back, err := expr.ParseCanonical(s)
		if err != nil || cmp.Diff(e, back) != "" {
			t.Fatalf("%s chain does not round trip: %v", op, err)
		}
	}
}

func TestChainPrintingKeepsParensWhereNeeded(t *testing.T) {
	a, b, c := ref("a"), ref("b"), ref("c")
	for want, e := range map[string]expr.Expr{
		"a && b && c":   bin("&&", bin("&&", a, b), c),
		"a && (b && c)": bin("&&", a, bin("&&", b, c)),
		"(a && b) || c": bin("||", bin("&&", a, b), c),
		"(a == b) == c": bin("==", bin("==", a, b), c),
		"a || b || c":   bin("||", bin("||", a, b), c),
		"(a || b) && c": bin("&&", bin("||", a, b), c),
	} {
		if got := expr.String(e); got != want {
			t.Errorf("String = %q want %q", got, want)
		}
		back, err := expr.ParseCanonical(want)
		if err != nil || cmp.Diff(e, back) != "" {
			t.Errorf("%q does not parse back to the AST: %v", want, err)
		}
	}
}
