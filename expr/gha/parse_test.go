package gha_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
)

func r(p ...string) expr.Expr                 { return expr.Ref{Path: p} }
func bin(op string, l, x expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: x} }
func str(s string) expr.Expr                  { return expr.Lit{Kind: expr.LitStr, Str: s} }
func num(f float64) expr.Expr                 { return expr.Lit{Kind: expr.LitNum, Num: f} }
func call(n string, a ...expr.Expr) expr.Expr { return expr.Call{Name: n, Args: a} }
func equalExpr(a, b expr.Expr) bool           { return reflect.DeepEqual(a, b) }

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		src  string
		want expr.Expr
		out  string
	}{
		{"github.ref == 'refs/heads/main'", bin(expr.OpEq, r("ref"), str("refs/heads/main")), ""},
		{"!cancelled() && (a || b)", bin(expr.OpAnd, expr.Unary{Op: "!", X: call("cancelled")},
			bin(expr.OpOr, r("gha", "a"), r("gha", "b"))), ""},
		{`contains(fromJSON('["a"]'), matrix.os)`, call("contains", call("fromJSON", str(`["a"]`)), r("matrix", "os")), ""},
		{"format('{0}-{1}', x, y)", call("format", str("{0}-{1}"), r("gha", "x"), r("gha", "y")), ""},
		{"0x1F", num(31), "31"},
		{"-1.5e3", num(-1500), "-1500"},
		{"1e21", num(1e21), "1e+21"},
		{"github.sha", r("sha"), ""},
		{"github.event_name == 'push'", bin(expr.OpEq, r("event"), str("push")), ""},
		{"github.event.pull_request.number", r("pr", "number"), ""},
		{"github.head_ref", r("pr", "head_ref"), ""},
		{"github.base_ref", r("pr", "base_ref"), ""},
		{"github.ref_name", r("ref_name"), ""},
		{"github.ref_type", r("ref_type"), ""},
		{"github.repository", r("repo"), ""},
		{"github.actor", r("actor"), ""},
		{"steps.build.outputs.v", r("steps", "build", "outputs", "v"), ""},
		{"needs.build.result", r("needs", "build", "result"), ""},
		{"env.X && secrets.Y && vars.Z && inputs.W", nil, "env.X && secrets.Y && vars.Z && inputs.W"},
		{"matrix['os']", r("matrix", "os"), "matrix.os"},
		{"needs['build linux'].result", r("needs", "build linux", "result"), ""},
		{"github.workspace", r("gha", "github", "workspace"), ""},
		{"runner.os == 'Linux'", bin(expr.OpEq, r("gha", "runner", "os"), str("Linux")), ""},
		{"github.ref_name.x", r("gha", "github", "ref_name", "x"), ""},
		{"a.true.null", r("gha", "a", "true", "null"), ""},
		{"true && null", bin(expr.OpAnd, expr.Lit{Kind: expr.LitBool, Bool: true}, expr.Lit{}), ""},
		{"'it''s'", str("it's"), ""},
		{"CONTAINS('a', 'b')", call("contains", str("a"), str("b")), "contains('a', 'b')"},
		{"hashFiles('**/go.sum')", call("hashFiles", str("**/go.sum")), ""},
		{"success() || FAILURE() || Always()", nil, "success() || failure() || always()"},
		{"  a  ==  b ", bin(expr.OpEq, r("gha", "a"), r("gha", "b")), "a == b"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			got, err := gha.Parse(c.src)
			if err != nil {
				t.Fatal(err)
			}
			if c.want != nil && !equalExpr(got, c.want) {
				t.Fatalf("Parse = %#v want %#v", got, c.want)
			}
			out, err := gha.Print(got)
			if err != nil {
				t.Fatal(err)
			}
			want := c.out
			if want == "" {
				want = strings.TrimSpace(c.src)
			}
			if out != want {
				t.Fatalf("Print = %q want %q", out, want)
			}
			back, err := gha.Parse(out)
			if err != nil || !equalExpr(back, got) {
				t.Fatalf("Parse(Print) = %#v, %v want %#v", back, err, got)
			}
		})
	}
}

func TestUnmappedRefKeepsOriginalSegmentsUnderGHARoot(t *testing.T) {
	got, err := gha.Parse("github.workspace")
	if err != nil || !equalExpr(got, r("gha", "github", "workspace")) {
		t.Fatalf("got %#v, %v", got, err)
	}
	out, err := gha.Print(r("gha", "github", "workspace"))
	if err != nil || out != "github.workspace" {
		t.Fatalf("Print = %q, %v", out, err)
	}
}

func TestOpaqueForUnmodelledSyntax(t *testing.T) {
	for _, src := range []string{
		"matrix[github.ref]", "matrix[0]", "github.event.*.name", "fruits.*", "fruits[*].name",
		"fromJSON(x).y", "fromJSON(x)['y']", "'abc'.length", "'abc'[0]", "(a).b",
		"nosuchfn(1)", "a && nosuchfn(b)", "NaN", "Infinity", "true.x", "!matrix[x]",
	} {
		got, err := gha.Parse(src)
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if o, ok := got.(expr.Opaque); !ok || o.Dialect != "gha" || o.Text != src {
			t.Errorf("%q = %#v, want Opaque with the whole source", src, got)
		}
	}
	got, _ := gha.Parse("  a[b]  ")
	if o := got.(expr.Opaque); o.Text != "a[b]" {
		t.Errorf("opaque text not trimmed: %q", o.Text)
	}
}

func TestParseError(t *testing.T) {
	cases := []struct{ src, pos, frag string }{
		{"", "position 1", "unexpected token"},
		{"a ==", "position 5", "unexpected token"},
		{"a b", "position 3", "unexpected token"},
		{"(a", "position 3", "')'"},
		{"f(a,", "position 5", "unexpected token"},
		{"a &", "position 3", "unexpected character"},
		{"a = b", "position 3", "unexpected character"},
		{`"x"`, "position 1", "unexpected character"},
		{"'abc", "position 1", "unterminated string"},
		{"a.", "position 3", "property name"},
		{"a.1", "position 3", "property name"},
		{"a[b", "position 4", "']'"},
		{"1a", "position 2", "after number"},
		{"1e999", "position 1", "out of range"},
		{"0x" + strings.Repeat("f", 300), "position 1", "out of range"},
		{"-x", "position 1", "unexpected character"},
		{"a ) b", "position 3", "unexpected token"},
		{"a || || b", "position 6", "unexpected token"},
		{strings.Repeat("!", 60) + "a", "position", "nested deeper"},
		{strings.Repeat("(", 60) + "a" + strings.Repeat(")", 60), "position", "nested deeper"},
	}
	for _, c := range cases {
		_, err := gha.Parse(c.src)
		if err == nil {
			t.Errorf("%q: want error", c.src)
			continue
		}
		if !strings.Contains(err.Error(), c.pos) || !strings.Contains(err.Error(), c.frag) {
			t.Errorf("%q: error %q lacks %q / %q", c.src, err, c.pos, c.frag)
		}
	}
}

func TestPrintErrors(t *testing.T) {
	cases := map[string]expr.Expr{
		"canonical branch":  r("branch"),
		"canonical tag":     r("tag"),
		"other provider":    r("gitlab", "CI_COMMIT_SHA"),
		"unknown canonical": r("pr", "title"),
		"empty ref":         expr.Ref{},
		"bare gha root":     r("gha"),
		"keyword root":      r("gha", "true"),
		"non-ident root":    r("gha", "a b"),
		"match op":          bin(expr.OpMatch, r("sha"), str("x")),
		"unknown fn":        call("nosuch"),
		"fn wrong case":     call("startswith", str("a"), str("b")),
		"foreign opaque":    expr.Opaque{Dialect: "gitlab", Text: "$X"},
		"nested foreign":    bin(expr.OpAnd, r("sha"), expr.Opaque{Dialect: "gitlab", Text: "$X"}),
		"non-finite":        expr.Lit{Kind: expr.LitNum, Num: 1 / zero()},
		"bad unary":         expr.Unary{Op: "-", X: r("sha")},
		"unknown lit kind":  expr.Lit{Kind: 9},
	}
	for name, e := range cases {
		if _, err := gha.Print(e); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	_, err := gha.Print(r("branch"))
	if err == nil || !strings.Contains(err.Error(), `"branch"`) {
		t.Errorf("error must name the ref: %v", err)
	}
}

func zero() float64 { return 0 }

func TestPrintOpaqueAndMinimalParens(t *testing.T) {
	if s, err := gha.Print(expr.Opaque{Dialect: "gha", Text: "a[b]"}); err != nil || s != "a[b]" {
		t.Fatalf("Print opaque = %q, %v", s, err)
	}
	e := bin(expr.OpAnd, expr.Opaque{Dialect: "gha", Text: "a || b"}, r("sha"))
	if s, err := gha.Print(e); err != nil || s != "(a || b) && github.sha" {
		t.Fatalf("nested opaque = %q, %v", s, err)
	}
}

func TestPrintStringsAndNumbers(t *testing.T) {
	for _, c := range []struct {
		e    expr.Expr
		want string
	}{
		{str("it's"), "'it''s'"}, {str(""), "''"}, {str(`say "hi"`), `'say "hi"'`},
		{num(0.1), "0.1"}, {num(-0.0299), "-0.0299"}, {num(1e-7), "1e-07"}, {num(123456789), "1.23456789e+08"},
	} {
		got, err := gha.Print(c.e)
		if err != nil || got != c.want {
			t.Errorf("Print = %q, %v want %q", got, err, c.want)
		}
		back, err := gha.Parse(got)
		if err != nil || !equalExpr(back, c.e) {
			t.Errorf("Parse(%q) = %#v, %v", got, back, err)
		}
	}
}

func TestVocabMappings(t *testing.T) {
	pairs := map[string]string{
		"github.ref_name": "ref_name", "github.ref_type": "ref_type", "github.ref": "ref", "github.sha": "sha",
		"github.event_name": "event", "github.repository": "repo", "github.actor": "actor",
		"github.event.pull_request.number": "pr.number", "github.head_ref": "pr.head_ref",
		"github.base_ref": "pr.base_ref", "matrix.os": "matrix.os", "env.A": "env.A", "secrets.S": "secrets.S",
		"vars.V": "vars.V", "inputs.I": "inputs.I", "needs.j.result": "needs.j.result", "steps.s.outcome": "steps.s.outcome",
	}
	for g, c := range pairs {
		gp := strings.Split(g, ".")
		cp := strings.Split(c, ".")
		if got, ok := gha.Vocab.Canon(gp); !ok || !reflect.DeepEqual(got, cp) {
			t.Errorf("Canon(%s) = %v, %v want %v", g, got, ok, cp)
		}
		if got, ok := gha.Vocab.Dialect(cp); !ok || !reflect.DeepEqual(got, gp) {
			t.Errorf("Dialect(%s) = %v, %v want %v", c, got, ok, gp)
		}
	}
	for _, c := range []string{"branch", "tag"} {
		if got, ok := gha.Vocab.Dialect([]string{c}); ok {
			t.Errorf("canonical %s must have no GHA ref, got %v", c, got)
		}
	}
}

func TestSemanticsValues(t *testing.T) {
	if !gha.Semantics.CaseInsensitiveEq || gha.Semantics.StrictTypes || !gha.Semantics.JSONNumbers || gha.Semantics.Truthy != nil {
		t.Fatalf("unexpected Semantics %+v", gha.Semantics)
	}
}

func TestEvalThroughCanonicalRefs(t *testing.T) {
	e := env{"ref_name": "Main", "matrix.os": "linux", "gha.github.workspace": "/w"}
	if got := evalSrc(t, "github.ref_name == 'main' && matrix.os == 'LINUX'", e); got != true {
		t.Errorf("got %v", got)
	}
	if got := evalSrc(t, "github.workspace", e); got != "/w" {
		t.Errorf("got %v", got)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"github.ref == 'refs/heads/main'", "!cancelled() && (a || b)", `contains(fromJSON('["a"]'), matrix.os)`,
		"format('{0}-{1}', x, y)", "0x1F", "-1.5e3", "a[b]", "a.*.c", "'it''s'", "a == b == c", "a && (b && c)",
		"!(a == b)", "a < b == c", "matrix['os']", "-0", "1e21", "github.event.pull_request.number", "(", "'", "a.", "STARTSWITH(x,'y')",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		e, err := gha.Parse(src)
		if err != nil {
			return
		}
		if _, ok := e.(expr.Opaque); ok {
			if _, err := gha.Print(e); err != nil {
				t.Fatalf("Print(opaque) %q: %v", src, err)
			}
			return
		}
		out, err := gha.Print(e)
		if err != nil {
			t.Fatalf("Print(Parse(%q)): %v", src, err)
		}
		back, err := gha.Parse(out)
		if err != nil || !equalExpr(back, e) {
			t.Fatalf("Parse(Print(%q)=%q) = %#v, %v want %#v", src, out, back, err, e)
		}
	})
}

// Nesting is one unit per paren, '!' or call level, however many operators sit at one level.
func TestParseDepthBoundary(t *testing.T) {
	m := expr.MaxDepth
	build := func(open, leaf, closer string, n int) string {
		return strings.Repeat(open, n) + leaf + strings.Repeat(closer, n)
	}
	cases := []struct {
		name string
		src  string
		ok   bool
	}{
		{"parens at max", build("(", "a", ")", m), true},
		{"parens over", build("(", "a", ")", m+1), false},
		{"unary at max", strings.Repeat("!", m) + "a", true},
		{"unary over", strings.Repeat("!", m+1) + "a", false},
		{"calls at max", build("format(", "a", ")", m), true},
		{"calls over", build("format(", "a", ")", m+1), false},
	}
	for _, c := range cases {
		_, err := gha.Parse(c.src)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// A chain longer than MaxDepth parses, validates and survives the canonical text, because a flat run of one
// operator is one nesting level.
func TestLongOperatorChainsAreCheckValid(t *testing.T) {
	for name, src := range map[string]string{
		"flat":  strings.Repeat("a || ", 200) + "a",
		"mixed": strings.Repeat("a || b && c == d || ", 200) + "a",
	} {
		e, err := gha.Parse(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := expr.Check(e); err != nil {
			t.Fatalf("%s: Check: %v", name, err)
		}
		back, err := expr.ParseCanonical(expr.String(e))
		if err != nil || !reflect.DeepEqual(e, back) {
			t.Fatalf("%s: canonical round trip failed: %v", name, err)
		}
	}
}

func TestParseAliases(t *testing.T) {
	want, err := gha.Parse("github.sha == 'x'")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		src     string
		aliases []string
	}{{"gitea.sha == 'x'", []string{"gitea"}}, {"forge.sha == 'x'", []string{"forgejo", "forge"}}} {
		got, err := gha.ParseWith(c.src, c.aliases)
		if err != nil {
			t.Fatal(err)
		}
		if !equalExpr(got, want) {
			t.Errorf("ParseWith(%q) = %#v, want %#v", c.src, got, want)
		}
	}
	got, err := gha.ParseWith("forgejo.ref_name", []string{"gitea"})
	if err != nil {
		t.Fatal(err)
	}
	if !equalExpr(got, r("gha", "forgejo", "ref_name")) {
		t.Errorf("alias outside the list must stay provider-native, got %#v", got)
	}
}

func TestAliasRejectedInGha(t *testing.T) {
	got, err := gha.Parse("gitea.sha")
	if err != nil {
		t.Fatal(err)
	}
	if !equalExpr(got, r("gha", "gitea", "sha")) {
		t.Errorf("got %#v", got)
	}
}

func TestParseTemplateWithAliases(t *testing.T) {
	parts, err := gha.ParseTemplateWith("echo ${{ forgejo.ref_name }}", []string{"forgejo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || !equalExpr(parts[1].Expr, r("ref_name")) {
		t.Errorf("got %#v", parts)
	}
}

func TestParseFoldsTheBranchAndTagShapesToCanonicalRefs(t *testing.T) {
	for src, want := range map[string]string{
		"github.ref_type == 'branch' && github.ref_name || ''": expr.CtxBranch,
		"github.ref_type == 'tag' && github.ref_name || ''":    expr.CtxTag,
	} {
		got, err := gha.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if r, ok := got.(expr.Ref); !ok || len(r.Path) != 1 || r.Path[0] != want {
			t.Errorf("Parse(%q) = %#v, want ref %s", src, got, want)
		}
	}
}
