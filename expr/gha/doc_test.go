package gha_test

// Rules pinned here come from the GitHub Actions expressions reference:
// https://docs.github.com/en/actions/reference/workflows-and-actions/expressions
// The page does not state operator precedence, function-name case or && / || return values; those
// come from the reference implementation (https://github.com/actions/languageservices,
// expressions/src/{parser,evaluator}.ts) and are marked below.

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
)

type env map[string]expr.Value

func (e env) Lookup(p []string) (expr.Value, bool) {
	v, ok := e[join(p)]
	return v, ok
}
func (env) Status() expr.Status { return expr.Success }

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

func evalSrc(t *testing.T, src string, e env) expr.Value {
	t.Helper()
	x, err := gha.Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	v, err := expr.Eval(x, e, gha.Semantics)
	if err != nil {
		t.Fatalf("Eval(%q): %v", src, err)
	}
	return v
}

func TestDocsStringComparisonIgnoresCase(t *testing.T) {
	// "GitHub ignores case when comparing strings."
	for src, want := range map[string]bool{
		`'ABC' == 'abc'`:                     true,
		`'ABC' != 'abc'`:                     false,
		`'a' < 'B'`:                          true,
		`'B' <= 'b'`:                         true,
		`contains('Hello', 'ELL')`:           true,
		`startsWith('Hello', 'hE')`:          true,
		`endsWith('Hello', 'LLO')`:           true,
		`contains(fromJSON('["Ab"]'), 'aB')`: true,
	} {
		if got := evalSrc(t, src, nil); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
	if gha.Semantics.CaseInsensitiveEq != true {
		t.Error("Semantics.CaseInsensitiveEq must be set")
	}
}

func TestDocsLooseEqualityCoercesToNumber(t *testing.T) {
	// "GitHub performs loose equality comparisons. If the types do not match, GitHub coerces the type to a number."
	// null 0; true 1, false 0; string parsed from any legal JSON number format, otherwise NaN, "" is 0.
	for src, want := range map[string]bool{
		`null == 0`:                        true,
		`null == ''`:                       true,
		`null == false`:                    true,
		`true == 1`:                        true,
		`false == 0`:                       true,
		`true == '1'`:                      true,
		`'' == 0`:                          true,
		`'1' == 1`:                         true,
		`'1.5e1' == 15`:                    true,
		`'abc' == 0`:                       false,
		`'0x10' == 16`:                     false,
		`'+1' == 1`:                        false,
		`'inf' == 0`:                       false,
		`'a' == 'a'`:                       true,
		`1 == 1`:                           true,
		`null == null`:                     true,
		`'abc' != 0`:                       true,
		`'abc' != 'abc'`:                   false,
		`'1' < 2`:                          true,
		`'abc' < 1`:                        false,
		`'abc' >= 1`:                       false,
		`'abc' > 1`:                        false,
		`'abc' <= 1`:                       false,
		`fromJSON('[]') == fromJSON('[]')`: false,
		`fromJSON('{}') == fromJSON('{}')`: false,
	} {
		if got := evalSrc(t, src, nil); got != want {
			t.Errorf("%s = %v, want %v", src, got, want)
		}
	}
}

func TestDocsFalsyValues(t *testing.T) {
	// "falsy values (false, 0, -0, "", '', null) are coerced to false"; everything else is truthy.
	for src, falsy := range map[string]bool{
		`false`: true, `0`: true, `-0`: true, `''`: true, `null`: true,
		`true`: false, `1`: false, `-1`: false, `'0'`: false, `'false'`: false, `' '`: false,
		`fromJSON('[]')`: false, `fromJSON('{}')`: false,
	} {
		if got := evalSrc(t, `!`+paren(src), nil); got != falsy {
			t.Errorf("!(%s) = %v, want %v", src, got, falsy)
		}
	}
}

func paren(s string) string { return "(" + s + ")" }

func TestDocsLiteralForms(t *testing.T) {
	// Boolean true/false, null, "any number format supported by JSON" plus the documented 0xff and -2.99e-2
	// examples, single-quoted strings with '' as the escape; double quotes "will throw an error".
	for src, want := range map[string]expr.Value{
		`true`: true, `false`: false, `null`: nil,
		`0xff`: 255.0, `0XfF`: 255.0, `-0x10`: -16.0, `-2.99e-2`: -0.0299, `1E3`: 1000.0, `1e+3`: 1000.0,
		`42`: 42.0, `-1.5e3`: -1500.0,
		`'It''s open source!'`: "It's open source!", `''`: "", `''''`: "'",
	} {
		if got := evalSrc(t, src, nil); got != want {
			t.Errorf("%s = %#v, want %#v", src, got, want)
		}
	}
	if _, err := gha.Parse(`"x"`); err == nil {
		t.Error("double-quoted string must be a syntax error")
	}
}

func TestOperatorPrecedenceBindsTighterToLooser(t *testing.T) {
	// Reference parser: || < && < (== !=) < (< <= > >=) < ! < index; all left-associative.
	for src, want := range map[string]string{
		`a || b && c`:   `a || b && c`,
		`(a || b) && c`: `(a || b) && c`,
		`a && b || c`:   `a && b || c`,
		`a == b && c`:   `a == b && c`,
		`a == (b && c)`: `a == (b && c)`,
		`a < b == c`:    `a < b == c`,
		`a < (b == c)`:  `a < (b == c)`,
		`a == b == c`:   `a == b == c`,
		`a == (b == c)`: `a == (b == c)`,
		`!a == b`:       `!a == b`,
		`!(a == b)`:     `!(a == b)`,
		`!!a`:           `!!a`,
		`a && (b && c)`: `a && (b && c)`,
		`(a && b) && c`: `a && b && c`,
		`((a))`:         `a`,
		`a||b`:          `a || b`,
	} {
		x, err := gha.Parse(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		got, err := gha.Print(x)
		if err != nil || got != want {
			t.Errorf("Print(Parse(%q)) = %q, %v want %q", src, got, err, want)
		}
	}
	// Tree shape for the cases where printing alone would hide a precedence bug.
	got, _ := gha.Parse(`a || b && c == d < e`)
	want := bin(expr.OpOr, r("gha", "a"), bin(expr.OpAnd, r("gha", "b"), bin(expr.OpEq, r("gha", "c"), bin(expr.OpLt, r("gha", "d"), r("gha", "e")))))
	if !equalExpr(got, want) {
		t.Errorf("tree = %#v", got)
	}
}

func TestFunctionNamesAreCaseInsensitive(t *testing.T) {
	// Reference evaluator lower-cases the function name before lookup.
	for src, want := range map[string]expr.Value{
		`STARTSWITH('Abc', 'a')`:           true,
		`Contains('abc', 'B')`:             true,
		`FORMAT('{0}-{1}', 'a', 'b')`:      "a-b",
		`ToJson(1)`:                        "1",
		`FROMJSON('[1]') != null`:          true,
		`JOIN(fromJSON('["a","b"]'), '+')`: "a+b",
	} {
		if got := evalSrc(t, src, nil); got != want {
			t.Errorf("%s = %#v, want %#v", src, got, want)
		}
	}
}

func TestLogicalOperatorsShortCircuitToOperandValue(t *testing.T) {
	// Reference evaluator stops at the first falsy (&&) or truthy (||) operand and returns it, not a bool.
	for src, want := range map[string]expr.Value{
		`'a' && 'b'`: "b", `'' && 'b'`: "", `null && 'b'`: nil,
		`'' || 'b'`: "b", `'a' || 'b'`: "a", `0 || null`: nil,
		`1 && 2 && 3`: 3.0, `0 || '' || 'z'`: "z",
	} {
		if got := evalSrc(t, src, nil); got != want {
			t.Errorf("%s = %#v, want %#v", src, got, want)
		}
	}
}

func TestRelationalComparisonWithNaNIsFalse(t *testing.T) {
	// "When NaN is one of the operands of any relational comparison (>, <, >=, <=), the result is always false."
	for _, op := range []string{"<", "<=", ">", ">="} {
		if got := evalSrc(t, `'x' `+op+` 1`, nil); got != false {
			t.Errorf("'x' %s 1 = %v", op, got)
		}
	}
	if got := evalSrc(t, `'x' == 'x'`, nil); got != true {
		t.Error("string equality is unaffected by NaN coercion")
	}
}
