package tekton_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/tekton"
)

func and(l, r expr.Expr) expr.Expr { return bin(expr.OpAnd, l, r) }

func or(l, r expr.Expr) expr.Expr { return bin(expr.OpOr, l, r) }

func TestWhenIdentity(t *testing.T) {
	for _, c := range []struct {
		e       expr.Expr
		name    string
		entries []tekton.Entry
	}{
		{eq("branch", "main"), "one value",
			[]tekton.Entry{{Input: "$(params.ctx-branch)", Operator: "in", Values: []string{"main"}}}},
		{or(or(eq("branch", "main"), eq("branch", "dev")), eq("branch", "rel")), "in several values",
			[]tekton.Entry{{Input: "$(params.ctx-branch)", Operator: "in", Values: []string{"main", "dev", "rel"}}}},
		{ne("branch", "main"), "notin one value",
			[]tekton.Entry{{Input: "$(params.ctx-branch)", Operator: "notin", Values: []string{"main"}}}},
		{and(ne("tag", "a"), ne("tag", "b")), "notin several values",
			[]tekton.Entry{{Input: "$(params.ctx-tag)", Operator: "notin", Values: []string{"a", "b"}}}},
		{and(eq("branch", "main"), eq("event", "push")), "entries are an AND",
			[]tekton.Entry{
				{Input: "$(params.ctx-branch)", Operator: "in", Values: []string{"main"}},
				{Input: "$(params.ctx-event)", Operator: "in", Values: []string{"push"}},
			}},
		{and(and(or(eq("branch", "a"), eq("branch", "b")), ne("tag", "x")), eq("repo", "r")), "three entries",
			[]tekton.Entry{
				{Input: "$(params.ctx-branch)", Operator: "in", Values: []string{"a", "b"}},
				{Input: "$(params.ctx-tag)", Operator: "notin", Values: []string{"x"}},
				{Input: "$(params.ctx-repo)", Operator: "in", Values: []string{"r"}},
			}},
		{bin(expr.OpEq, ref("inputs", "env"), str("prod")), "input param",
			[]tekton.Entry{{Input: "$(params.env)", Operator: "in", Values: []string{"prod"}}}},
		{bin(expr.OpEq, ref("needs", "build", "outputs", "ok"), str("yes")), "task result",
			[]tekton.Entry{{Input: "$(tasks.build.results.ok)", Operator: "in", Values: []string{"yes"}}}},
		{eq("branch", ""), "empty literal",
			[]tekton.Entry{{Input: "$(params.ctx-branch)", Operator: "in", Values: []string{""}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := tekton.ToWhen(c.e)
			if !ok || !reflect.DeepEqual(got, c.entries) {
				t.Fatalf("ToWhen = %#v, %v; want %#v", got, ok, c.entries)
			}
			if back := tekton.FromWhen(c.entries); !reflect.DeepEqual(back, c.e) {
				t.Errorf("FromWhen = %#v; want %#v", back, c.e)
			}
		})
	}
}

func TestToWhenAcceptsLiteralOnTheLeft(t *testing.T) {
	got, ok := tekton.ToWhen(bin(expr.OpEq, str("main"), ref("branch")))
	want := []tekton.Entry{{Input: "$(params.ctx-branch)", Operator: "in", Values: []string{"main"}}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("ToWhen = %#v, %v", got, ok)
	}
}

// A negated equality is a notin entry, which reads back as an inequality.
func TestToWhenNegatedEquality(t *testing.T) {
	got, ok := tekton.ToWhen(expr.Unary{Op: expr.OpNot, X: eq("branch", "a")})
	want := []tekton.Entry{{Input: "$(params.ctx-branch)", Operator: "notin", Values: []string{"a"}}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("ToWhen = %#v, %v; want %#v", got, ok, want)
	}
	if back := tekton.FromWhen(got); !reflect.DeepEqual(back, ne("branch", "a")) {
		t.Errorf("FromWhen = %#v", back)
	}
}

func TestToWhenRefusals(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"or of two refs":        or(eq("branch", "a"), eq("tag", "b")),
		"or of inequalities":    or(ne("branch", "a"), ne("branch", "b")),
		"mixed chain":           or(eq("branch", "a"), ne("branch", "b")),
		"not of a call":         expr.Unary{Op: expr.OpNot, X: expr.Call{Name: "success"}},
		"or of and":             or(and(eq("branch", "a"), eq("tag", "b")), eq("repo", "c")),
		"call":                  expr.Call{Name: "success"},
		"bare ref":              ref("branch"),
		"ordering":              bin(expr.OpLt, ref("branch"), str("x")),
		"regex":                 bin(expr.OpMatch, ref("branch"), str("^x")),
		"number literal":        bin(expr.OpEq, ref("branch"), expr.Lit{Kind: expr.LitNum, Num: 1}),
		"null literal":          bin(expr.OpEq, ref("branch"), expr.Lit{}),
		"ref against ref":       bin(expr.OpEq, ref("branch"), ref("tag")),
		"env ref":               bin(expr.OpEq, ref("env", "X"), str("y")),
		"secret ref":            bin(expr.OpEq, ref("secrets", "X"), str("y")),
		"matrix ref":            bin(expr.OpEq, ref("matrix", "os"), str("linux")),
		"matrix ref in and":     and(eq("branch", "a"), bin(expr.OpNe, ref("matrix", "os"), str("linux"))),
		"vars ref":              bin(expr.OpEq, ref("vars", "X"), str("y")),
		"opaque":                expr.Opaque{Dialect: tekton.Dialect, Text: "x"},
		"opaque inside and":     and(eq("branch", "a"), expr.Opaque{Dialect: "gha", Text: "x"}),
		"unknown context name":  eq("workflow", "a"),
		"literal against lit":   bin(expr.OpEq, str("a"), str("a")),
		"unsupported key shape": bin(expr.OpEq, ref("needs", "build"), str("a")),
	} {
		if got, ok := tekton.ToWhen(e); ok {
			t.Errorf("%s: ToWhen = %#v; want a refusal", name, got)
		}
	}
}

func TestFromWhenOpaque(t *testing.T) {
	for _, c := range []struct {
		name  string
		entry tekton.Entry
	}{
		{"cel entry", tekton.Entry{CEL: "'$(params.ctx-branch)'.matches('release/.*')"}},
		{"literal input", tekton.Entry{Input: "main", Operator: "in", Values: []string{"main"}}},
		{"empty input", tekton.Entry{Operator: "in", Values: []string{"x"}}},
		{"two refs in one input", tekton.Entry{Input: "$(params.a)-$(params.b)", Operator: "in", Values: []string{"x"}}},
		{"context variable", tekton.Entry{Input: "$(context.pipelineRun.name)", Operator: "in", Values: []string{"x"}}},
		{"secret-like input", tekton.Entry{Input: "$(params.a)$(params.b)", Operator: "notin", Values: []string{"x"}}},
		{"unknown operator", tekton.Entry{Input: "$(params.a)", Operator: "equals", Values: []string{"x"}}},
		{"no values", tekton.Entry{Input: "$(params.a)", Operator: "in"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := tekton.FromWhen([]tekton.Entry{c.entry})
			o, ok := e.(expr.Opaque)
			if !ok || o.Dialect != tekton.Dialect || o.Text == "" {
				t.Fatalf("FromWhen = %#v; want Opaque{tekton}", e)
			}
			if _, ok := tekton.ToWhen(e); ok {
				t.Error("ToWhen accepted an Opaque")
			}
		})
	}
}

func TestFromWhenOpaqueKeepsTheEntryApart(t *testing.T) {
	e := tekton.FromWhen([]tekton.Entry{
		{Input: "$(params.ctx-branch)", Operator: "in", Values: []string{"main"}},
		{CEL: "true"},
	})
	b, ok := e.(expr.Binary)
	if !ok || b.Op != expr.OpAnd || !expr.HasOpaque(e) {
		t.Fatalf("FromWhen = %#v; want an AND with an Opaque", e)
	}
	if !reflect.DeepEqual(b.L, eq("branch", "main")) {
		t.Errorf("left = %#v", b.L)
	}
}

func TestFromWhenEmpty(t *testing.T) {
	if e := tekton.FromWhen(nil); e != nil {
		t.Errorf("FromWhen(nil) = %#v; want nil", e)
	}
}

func TestDisabledWhen(t *testing.T) {
	d := tekton.DisabledWhen()
	want := []tekton.Entry{{Input: "pipemaster-disabled", Operator: "in", Values: []string{"never"}}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("DisabledWhen = %#v; want %#v", d, want)
	}
	if !tekton.IsDisabled(d) {
		t.Error("IsDisabled(DisabledWhen) = false")
	}
	if tekton.IsDisabled(append(d, tekton.Entry{CEL: "x"})) || tekton.IsDisabled(nil) {
		t.Error("IsDisabled accepted another list")
	}
	if _, ok := tekton.FromWhen(d).(expr.Opaque); !ok {
		t.Error("the disabled when must read as Opaque")
	}
}

func TestOpaqueTextIsJSONOfTheEntry(t *testing.T) {
	in := tekton.Entry{Input: "x $(params.a)", Operator: "in", Values: []string{"a\"b", "$(id)"}}
	o := tekton.FromWhen([]tekton.Entry{in}).(expr.Opaque)
	var back tekton.Entry
	if err := json.Unmarshal([]byte(o.Text), &back); err != nil || !reflect.DeepEqual(back, in) {
		t.Errorf("Opaque text %q does not hold the entry: %#v, %v", o.Text, back, err)
	}
	if got, ok := tekton.OpaqueEntry(o); !ok || !reflect.DeepEqual(got, in) {
		t.Errorf("OpaqueEntry = %#v, %v", got, ok)
	}
	if _, ok := tekton.OpaqueEntry(expr.Opaque{Dialect: "gha", Text: o.Text}); ok {
		t.Error("OpaqueEntry accepted another dialect")
	}
	if _, ok := tekton.OpaqueEntry(expr.Opaque{Dialect: tekton.Dialect, Text: "{"}); ok {
		t.Error("OpaqueEntry accepted bad text")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"$(params.ctx-branch)", "$(tasks.a.results.b)", "$(params.x)$(params.y)", "$(params.", "$(", "", "main",
		"$(params.ctx-pr-number)", "$(tasks.status)",
	} {
		f.Add(s, "in", "a")
		f.Add(s, "notin", "")
	}
	f.Fuzz(func(t *testing.T, input, op, value string) {
		entries := []tekton.Entry{{Input: input, Operator: op, Values: []string{value, value + "b"}}}
		e := tekton.FromWhen(entries)
		if e == nil {
			t.Fatal("FromWhen returned nil for an entry")
		}
		back, ok := tekton.ToWhen(e)
		if !ok {
			if !expr.HasOpaque(e) {
				t.Fatalf("ToWhen refused a decoded condition %#v", e)
			}
			return
		}
		if again := tekton.FromWhen(back); !reflect.DeepEqual(again, e) {
			t.Fatalf("round trip changed %#v to %#v", e, again)
		}
	})
}
