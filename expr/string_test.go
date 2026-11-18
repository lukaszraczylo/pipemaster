package expr_test

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir/irtest"
	"pgregory.net/rapid"
)

func TestStringGolden(t *testing.T) {
	cases := []struct {
		e    expr.Expr
		want string
	}{
		{ref("branch"), "branch"},
		{ref("matrix", "os"), "matrix.os"},
		{ref("needs", "build-linux", "outputs", "v"), "needs.build-linux.outputs.v"},
		{ref("needs", "build linux", "outputs"), "needs['build linux'].outputs"},
		{ref("true"), "['true']"},
		{ref("true", "x"), "['true'].x"},
		{ref("a", "9"), "a['9']"},
		{str("it's"), "'it''s'"},
		{str(""), "''"},
		{num(1), "1"},
		{num(0.1), "0.1"},
		{num(1e21), "1e+21"},
		{num(-2.5), "-2.5"},
		{null(), "null"},
		{boolean(true), "true"},
		{boolean(false), "false"},
		{not(ref("a")), "!a"},
		{not(bin("==", ref("a"), num(1))), "!(a == 1)"},
		{bin("==", ref("branch"), str("main")), "branch == 'main'"},
		{bin("&&", bin("||", ref("a"), ref("b")), ref("c")), "(a || b) && c"},
		{bin("||", ref("a"), bin("&&", ref("b"), ref("c"))), "a || (b && c)"},
		{bin("==", not(ref("a")), ref("b")), "!a == b"},
		{call("contains", ref("arr"), str("x")), "contains(arr, 'x')"},
		{call("success"), "success()"},
		{expr.Opaque{Dialect: "gha", Text: "hashFiles('x')"}, "opaque(gha): hashFiles('x')"},
		{bin("&&", ref("a"), expr.Opaque{Dialect: "gha", Text: "x"}), "a && @opaque('gha', 'x')"},
		{expr.Opaque{Dialect: "a)b", Text: "x"}, "@opaque('a)b', 'x')"},
	}
	for _, c := range cases {
		if got := expr.String(c.e); got != c.want {
			t.Errorf("String = %q want %q", got, c.want)
		}
		back, err := expr.ParseCanonical(c.want)
		if err != nil {
			t.Errorf("ParseCanonical(%q): %v", c.want, err)
			continue
		}
		if d := cmp.Diff(c.e, back); d != "" {
			t.Errorf("ParseCanonical(%q) mismatch: %s", c.want, d)
		}
	}
}

func TestParseCanonicalHandWritten(t *testing.T) {
	cases := map[string]expr.Expr{
		"a || b && c":      bin("||", ref("a"), bin("&&", ref("b"), ref("c"))),
		"a == b && c < 1":  bin("&&", bin("==", ref("a"), ref("b")), bin("<", ref("c"), num(1))),
		"a == b == c":      bin("==", bin("==", ref("a"), ref("b")), ref("c")),
		"  !  a  ":         not(ref("a")),
		"!!a":              not(not(ref("a"))),
		"a =~ '^x'":        bin("=~", ref("a"), str("^x")),
		"a !~ 'x'":         bin("!~", ref("a"), str("x")),
		"a != b":           bin("!=", ref("a"), ref("b")),
		"a <= 1 || a >= 2": bin("||", bin("<=", ref("a"), num(1)), bin(">=", ref("a"), num(2))),
		"1e3":              num(1000),
		"-5":               num(-5),
		"f( 1 , 'x' )":     call("f", num(1), str("x")),
	}
	for in, want := range cases {
		got, err := expr.ParseCanonical(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if d := cmp.Diff(want, got); d != "" {
			t.Errorf("%q: %s", in, d)
		}
	}
}

func TestParseCanonicalErrors(t *testing.T) {
	for _, in := range []string{
		"", "   ", "a &&", "(a", "a)", "'abc", "a b", "a.", "a..b", "a['x'", "f(1,)", "f(1 2)", "1.2.3", "1e", "+Inf",
		"a ==", "== a", "@opaque('x')", "@nope('a','b')", "a['x' 'y']", "$x", "a = b", "a & b", "a | b",
	} {
		if e, err := expr.ParseCanonical(in); err == nil {
			t.Errorf("%q: want error, got %#v", in, e)
		}
	}
}

func TestStringNumberExact(t *testing.T) {
	for _, f := range []float64{0, math.Copysign(0, -1), 1, -1, 0.1, 1.0 / 3, 1e21, 1e-7, 123456789012345678, math.MaxFloat64, math.SmallestNonzeroFloat64, -math.MaxFloat64} {
		back, err := expr.ParseCanonical(expr.String(num(f)))
		if err != nil {
			t.Fatal(f, err)
		}
		got := back.(expr.Lit).Num
		if math.Float64bits(got) != math.Float64bits(f) {
			t.Fatalf("%v round-tripped to %v", f, got)
		}
	}
}

func TestStringParseRoundTripRapid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := irtest.GenExpr(t, "e", 4, true)
		s := expr.String(e)
		back, err := expr.ParseCanonical(s)
		if err != nil {
			t.Fatalf("ParseCanonical(%q): %v", s, err)
		}
		if d := cmp.Diff(e, back); d != "" {
			t.Fatalf("round trip of %q differs: %s", s, d)
		}
		if again := expr.String(back); again != s {
			t.Fatalf("String not stable: %q vs %q", s, again)
		}
	})
}

func TestHasOpaque(t *testing.T) {
	op := expr.Opaque{Dialect: "g", Text: "x"}
	for _, c := range []struct {
		e    expr.Expr
		want bool
	}{
		{nil, false}, {ref("a"), false}, {op, true}, {bin("&&", ref("a"), not(op)), true}, {call("f", ref("a"), op), true}, {call("f"), false},
	} {
		e, want := c.e, c.want
		if got := expr.HasOpaque(e); got != want {
			t.Errorf("HasOpaque(%#v) = %v", e, got)
		}
	}
}
