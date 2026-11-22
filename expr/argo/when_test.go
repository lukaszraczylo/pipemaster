package argo_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/argo"
)

const (
	branchRef = "workflow.parameters['ctx-branch']"
	needsRef  = "tasks['build'].outputs.parameters['digest']"
)

func wrap(body string) string { return "{{= " + body + " }}" }

// printCases is the identity table: each AST prints to the text and the text reads back to the AST.
var printCases = []struct {
	e    expr.Expr
	name string
	want string
}{
	{eq("branch", "main"), "context ref", wrap(branchRef + " == 'main'")},
	{ne("branch", "main"), "inequality", wrap(branchRef + " != 'main'")},
	{bin(expr.OpEq, ref("pr", "number"), str("7")), "dotted context name", wrap("workflow.parameters['ctx-pr-number'] == '7'")},
	{eq("ref_name", "x"), "underscore context name", wrap("workflow.parameters['ctx-ref-name'] == 'x'")},
	{bin(expr.OpEq, ref("inputs", "deploy-env"), str("prod")), "input", wrap("workflow.parameters['deploy-env'] == 'prod'")},
	{bin(expr.OpEq, ref("needs", "build", "outputs", "digest"), str("x")), "needs output", wrap("(" + needsRef + " ?? '') == 'x'")},
	{bin(expr.OpEq, ref("matrix", "os"), str("linux")), "matrix axis", wrap("item['os'] == 'linux'")},
	{bin(expr.OpEq, str("a"), ref("inputs", "x")), "literal first", wrap("'a' == workflow.parameters['x']")},
	{bin(expr.OpEq, ref("inputs", "a"), ref("inputs", "b")), "two refs", wrap("workflow.parameters['a'] == workflow.parameters['b']")},
	{and(eq("branch", "a"), eq("tag", "b")), "and", wrap(branchRef + " == 'a' && workflow.parameters['ctx-tag'] == 'b'")},
	{or(eq("branch", "a"), eq("branch", "b")), "or", wrap(branchRef + " == 'a' || " + branchRef + " == 'b'")},
	{and(and(eq("branch", "a"), eq("tag", "b")), eq("repo", "c")), "and chain",
		wrap(branchRef + " == 'a' && workflow.parameters['ctx-tag'] == 'b' && workflow.parameters['ctx-repo'] == 'c'")},
	{and(eq("branch", "a"), and(eq("tag", "b"), eq("repo", "c"))), "right nested and keeps its group",
		wrap(branchRef + " == 'a' && (workflow.parameters['ctx-tag'] == 'b' && workflow.parameters['ctx-repo'] == 'c')")},
	{or(and(eq("branch", "a"), eq("tag", "b")), ne("repo", "c")), "mixed operators group",
		wrap("(" + branchRef + " == 'a' && workflow.parameters['ctx-tag'] == 'b') || workflow.parameters['ctx-repo'] != 'c'")},
	{and(or(eq("branch", "a"), eq("branch", "b")), eq("tag", "c")), "or under and",
		wrap("(" + branchRef + " == 'a' || " + branchRef + " == 'b') && workflow.parameters['ctx-tag'] == 'c'")},
	{not(eq("branch", "a")), "not", wrap("!(" + branchRef + " == 'a')")},
	{not(not(eq("branch", "a"))), "double not", wrap("!!(" + branchRef + " == 'a')")},
	{not(boolean(true)), "not bool", wrap("!true")},
	{boolean(false), "false", wrap("false")},
	{boolean(true), "true", wrap("true")},
	{bin(expr.OpEq, ref("inputs", "n"), num(3)), "integer", wrap("workflow.parameters['n'] == 3")},
	{bin(expr.OpEq, ref("inputs", "n"), num(2.5)), "fraction", wrap("workflow.parameters['n'] == 2.5")},
	{eq("branch", "it's"), "single quote", wrap(branchRef + ` == 'it\'s'`)},
	{eq("branch", `a\b`), "backslash", wrap(branchRef + ` == 'a\\b'`)},
	{eq("branch", `a "b" c`), "double quote", wrap(branchRef + ` == 'a "b" c'`)},
	{eq("branch", "a\nb\tc\r"), "control characters", wrap(branchRef + ` == 'a\nb\tc\r'`)},
	{eq("branch", "a\x00\x7f"), "nul and del", wrap(branchRef + ` == 'a\x00\x7f'`)},
	{eq("branch", "café ☃"), "unicode", wrap(branchRef + " == 'café ☃'")},
	{eq("branch", ""), "empty literal", wrap(branchRef + " == ''")},
	{eq("branch", "$(params.x) ${y} `z` # \"{ }\""), "shell and brace text", wrap(branchRef + " == '$(params.x) ${y} `z` # \"{ }\"'")},
	{eq("branch", "{ {"), "split braces", wrap(branchRef + " == '{ {'")},
}

func TestToWhen(t *testing.T) {
	for _, c := range printCases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := argo.ToWhen(c.e, argo.ScopeTask)
			if !ok || got != c.want {
				t.Fatalf("ToWhen = %q, %v; want %q", got, ok, c.want)
			}
			back := argo.FromWhen(got, argo.ScopeTask)
			if !reflect.DeepEqual(back, c.e) {
				t.Errorf("FromWhen(%q) = %#v; want %#v", got, back, c.e)
			}
		})
	}
}

func TestTemplateScopeMatrix(t *testing.T) {
	e := bin(expr.OpEq, ref("matrix", "os"), str("linux"))
	const want = "{{= inputs.parameters['os'] == 'linux' }}"
	got, ok := argo.ToWhen(e, argo.ScopeTemplate)
	if !ok || got != want {
		t.Fatalf("ToWhen = %q, %v; want %q", got, ok, want)
	}
	if back := argo.FromWhen(got, argo.ScopeTemplate); !reflect.DeepEqual(back, e) {
		t.Errorf("FromWhen = %#v", back)
	}
	// In the task scope the same text names a template input, which has no canonical ref.
	if _, isOpaque := argo.FromWhen(got, argo.ScopeTask).(expr.Opaque); !isOpaque {
		t.Error("task scope read a template input as a matrix ref")
	}
	// Other tasks are not visible inside the template.
	if w, ok := argo.ToWhen(bin(expr.OpEq, ref("needs", "a", "outputs", "b"), str("x")), argo.ScopeTemplate); ok {
		t.Errorf("template scope printed a needs ref: %q", w)
	}
}

func TestToWhenRefusals(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"nil":                nil,
		"call":               expr.Call{Name: "success"},
		"nested opaque":      and(eq("branch", "a"), expr.Opaque{Dialect: argo.Dialect, Text: "{{= x }}"}),
		"foreign opaque":     expr.Opaque{Dialect: "tekton", Text: "{}"},
		"bare ref":           ref("branch"),
		"bare string":        str("x"),
		"ordering":           bin(expr.OpLt, ref("branch"), str("x")),
		"match":              bin(expr.OpMatch, ref("branch"), str("^v")),
		"null literal":       bin(expr.OpEq, ref("branch"), expr.Lit{}),
		"env ref":            bin(expr.OpEq, ref("env", "X"), str("y")),
		"secret ref":         bin(expr.OpEq, ref("secrets", "X"), str("y")),
		"vars ref":           bin(expr.OpEq, ref("vars", "X"), str("y")),
		"steps ref":          bin(expr.OpEq, ref("steps", "a", "outcome"), str("y")),
		"unknown context":    eq("workflow", "a"),
		"input with quote":   bin(expr.OpEq, ref("inputs", "a'b"), str("y")),
		"input with bracket": bin(expr.OpEq, ref("inputs", "a']"), str("y")),
		"empty input name":   bin(expr.OpEq, ref("inputs", ""), str("y")),
		"needs no output":    bin(expr.OpEq, ref("needs", "a"), str("y")),
		"needs upper task":   bin(expr.OpEq, ref("needs", "A", "outputs", "b"), str("y")),
		"negative number":    bin(expr.OpEq, ref("inputs", "n"), num(-1)),
		"not a ref":          not(ref("branch")),
		"not a string":       not(str("x")),
		"open tag":           eq("branch", "a{{b"),
		"close tag":          eq("branch", "a}}b"),
		"tag":                eq("branch", "{{workflow.name}}"),
		"invalid utf8":       eq("branch", "a\xffb"),
		"unary other":        expr.Unary{Op: "-", X: boolean(true)},
	} {
		if w, ok := argo.ToWhen(e, argo.ScopeTask); ok {
			t.Errorf("%s: ToWhen = %q; want a refusal", name, w)
		}
	}
}

// parseCases reads text that the printer does not write: the govaluate form, spelling variants and the
// shapes outside the subset, which become one Opaque node that holds the text.
func TestFromWhen(t *testing.T) {
	opaque := func(s string) expr.Expr { return expr.Opaque{Dialect: argo.Dialect, Text: s} }
	for _, c := range []struct {
		want expr.Expr
		name string
		when string
	}{
		{nil, "empty", ""},
		{nil, "blank", "  \t"},
		{eq("branch", "main"), "govaluate bare word", "{{workflow.parameters.ctx-branch}} == main"},
		{eq("branch", "main"), "govaluate spaced tag", "{{ workflow.parameters.ctx-branch }} == main"},
		{ne("branch", "main"), "govaluate inequality", "{{workflow.parameters.ctx-branch}} != main"},
		{eq("branch", "main"), "govaluate single quotes", "{{workflow.parameters.ctx-branch}} == 'main'"},
		{eq("branch", "main"), "govaluate double quotes", `{{workflow.parameters.ctx-branch}} == "main"`},
		{eq("branch", "feature_x"), "govaluate underscore word", "{{workflow.parameters.ctx-branch}} == feature_x"},
		{eq("branch", "v1"), "govaluate letter then digit", "{{workflow.parameters.ctx-branch}} == v1"},
		{bin(expr.OpEq, ref("needs", "build", "outputs", "digest"), str("ok")), "govaluate needs output", "{{tasks.build.outputs.parameters.digest}} == ok"},
		{bin(expr.OpEq, ref("needs", "build", "outputs", "digest"), str("x")), "needs plain form", "{{= " + needsRef + " == 'x' }}"},
		{bin(expr.OpEq, ref("matrix", "os"), str("linux")), "govaluate item", "{{item.os}} == linux"},
		{bin(expr.OpEq, ref("inputs", "a"), ref("inputs", "b")), "govaluate two tags", "{{workflow.parameters.a}} == {{workflow.parameters.b}}"},
		{or(and(ne("branch", "a"), eq("tag", "b")), not(eq("repo", "c"))), "govaluate logic",
			"({{workflow.parameters.ctx-branch}} != a && {{workflow.parameters.ctx-tag}} == b) || !({{workflow.parameters.ctx-repo}} == c)"},
		{eq("branch", "main"), "expr without spaces", "{{=" + branchRef + "=='main'}}"},
		{eq("branch", "main"), "expr double quotes", `{{= workflow.parameters["ctx-branch"] == "main" }}`},
		{bin(expr.OpEq, ref("inputs", "deploy_env"), str("main")), "expr dotted name", "{{= workflow.parameters.deploy_env == 'main' }}"},
		{and(eq("branch", "a"), not(boolean(false))), "expr and not", "{{= " + branchRef + " == 'a' and not false }}"},
		{or(eq("branch", "a"), eq("tag", "b")), "expr or word", "{{= " + branchRef + " == 'a' or workflow.parameters['ctx-tag'] == 'b' }}"},
		{eq("branch", "a'b"), "expr escaped quote", `{{= ` + branchRef + ` == 'a\'b' }}`},
		{eq("branch", `a"b`), "expr escaped double", `{{= ` + branchRef + ` == "a\"b" }}`},
		{eq("branch", "aé\n"), "expr unicode escapes", `{{= ` + branchRef + ` == 'aé\x0a' }}`},
		{bin(expr.OpEq, ref("needs", "build", "outputs", "digest"), str("x")), "needs default", "{{= (" + needsRef + " ?? '') == 'x' }}"},
		{bin(expr.OpEq, ref("needs", "build", "outputs", "digest"), str("x")), "needs default double quotes", "{{= (" + needsRef + " ?? \"\") == 'x' }}"},
		{boolean(false), "disabled without spaces", "{{=false}}"},

		{opaque("{{= " + branchRef + " =~ '^v' }}"), "regex", "{{= " + branchRef + " =~ '^v' }}"},
		{opaque("{{workflow.parameters.ctx-branch}} =~ ^v"), "govaluate regex", "{{workflow.parameters.ctx-branch}} =~ ^v"},
		{opaque("{{= sprig.trim(" + branchRef + ") == 'a' }}"), "function", "{{= sprig.trim(" + branchRef + ") == 'a' }}"},
		{opaque("{{= " + branchRef + " > 'a' }}"), "ordering", "{{= " + branchRef + " > 'a' }}"},
		{opaque("{{= workflow.status == 'Failed' }}"), "workflow status", "{{= workflow.status == 'Failed' }}"},
		{opaque("{{= workflow.parameters.ctx-branch == 'x' }}"), "hyphen is a minus", "{{= workflow.parameters.ctx-branch == 'x' }}"},
		{opaque("{{= " + branchRef + " }}"), "bare ref", "{{= " + branchRef + " }}"},
		{opaque("{{= !" + branchRef + " == 'a' }}"), "not binds tighter than ==", "{{= !" + branchRef + " == 'a' }}"},
		{opaque("{{= (" + branchRef + " == 'a' }}"), "open paren", "{{= (" + branchRef + " == 'a' }}"},
		{opaque("{{= " + branchRef + " == 'a }}"), "open string", "{{= " + branchRef + " == 'a }}"},
		{opaque("{{= " + branchRef + " == 'a' == true }}"), "chained comparison", "{{= " + branchRef + " == 'a' == true }}"},
		{opaque("{{= " + branchRef + " ?? '' == 'a' }}"), "default on a context ref", "{{= " + branchRef + " ?? '' == 'a' }}"},
		{opaque("{{= " + needsRef + " ?? '' == 'a' }}"), "default without a group binds looser than ==", "{{= " + needsRef + " ?? '' == 'a' }}"},
		{opaque("{{= (" + needsRef + " ?? 'x') == 'a' }}"), "default not empty", "{{= (" + needsRef + " ?? 'x') == 'a' }}"},
		{opaque("{{= env.X == 'a' }}"), "unknown variable", "{{= env.X == 'a' }}"},
		{opaque("{{= " + branchRef + " == 'a\\q' }}"), "unknown escape", "{{= " + branchRef + " == 'a\\q' }}"},
		{opaque("{{= " + branchRef + " == `a` }}"), "raw string", "{{= " + branchRef + " == `a` }}"},
		{opaque("{{= " + branchRef + " == 'a' }} && x"), "tag then text", "{{= " + branchRef + " == 'a' }} && x"},
		{opaque("{{= " + branchRef + " == 'a' }}{{= true }}"), "two expression tags", "{{= " + branchRef + " == 'a' }}{{= true }}"},
		{opaque("{{workflow.parameters.ctx-branch}} == {{= 'a' }}"), "expression tag inside govaluate", "{{workflow.parameters.ctx-branch}} == {{= 'a' }}"},
		{opaque("main == {{workflow.parameters.ctx-branch}}"), "bare word on the left", "main == {{workflow.parameters.ctx-branch}}"},
		{opaque("{{workflow.parameters.ctx-branch}} == a+b"), "operator in a bare word", "{{workflow.parameters.ctx-branch}} == a+b"},
		{opaque("{{workflow.parameters.ctx-branch}} == 'a\\'b'"), "govaluate escape", "{{workflow.parameters.ctx-branch}} == 'a\\'b'"},
		{opaque("{{workflow.name}} == x"), "govaluate unknown tag", "{{workflow.name}} == x"},
		{opaque("{{workflow.parameters.json}} == x"), "all parameters as json", "{{workflow.parameters.json}} == x"},
		{opaque("{{workflow.parameters.ctx-branch}} == x }}"), "stray close", "{{workflow.parameters.ctx-branch}} == x }}"},
		{opaque("{{workflow.parameters.ctx-branch == x"), "open tag", "{{workflow.parameters.ctx-branch == x"},
		{opaque("{{workflow.parameters.x}} == feature-x"), "govaluate minus", "{{workflow.parameters.x}} == feature-x"},
		{opaque("{{workflow.parameters.x}} == dev-1"), "govaluate minus digit", "{{workflow.parameters.x}} == dev-1"},
		{opaque("{{workflow.parameters.x}} == a/b"), "govaluate divide", "{{workflow.parameters.x}} == a/b"},
		{opaque("{{workflow.parameters.x}} == release.v1"), "govaluate accessor", "{{workflow.parameters.x}} == release.v1"},
		{opaque("{{workflow.parameters.x}} == v1.2"), "govaluate accessor number", "{{workflow.parameters.x}} == v1.2"},
		{opaque("{{workflow.parameters.x}} == a:b"), "govaluate ternary", "{{workflow.parameters.x}} == a:b"},
		{opaque("{{workflow.parameters.x}} == a@b"), "govaluate invalid token", "{{workflow.parameters.x}} == a@b"},
		{opaque("{{workflow.parameters.x}} == 1.50"), "govaluate numeric word", "{{workflow.parameters.x}} == 1.50"},
		{opaque("{{workflow.parameters.x}} == 5"), "govaluate integer word", "{{workflow.parameters.x}} == 5"},
		{opaque("{{workflow.parameters.x}} == 1abc"), "govaluate digit start", "{{workflow.parameters.x}} == 1abc"},
		{opaque("{{workflow.parameters.x}} == true"), "govaluate true word", "{{workflow.parameters.x}} == true"},
		{opaque("{{workflow.parameters.x}} == false"), "govaluate false word", "{{workflow.parameters.x}} == false"},
		{opaque("{{workflow.parameters.x}} == in"), "govaluate in word", "{{workflow.parameters.x}} == in"},
		{opaque("{{workflow.parameters.x}} == IN"), "govaluate IN word", "{{workflow.parameters.x}} == IN"},
		{opaque("{{=0"), "unterminated expression tag", "{{=0"},
		{opaque("{{=}}"), "empty expression tag", "{{=}}"},
		{opaque("main"), "plain word", "main"},
		{opaque("{{workflow.parameters.ctx-branch}}"), "bare tag", "{{workflow.parameters.ctx-branch}}"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := argo.FromWhen(c.when, argo.ScopeTask)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("FromWhen(%q) =\n%#v\nwant\n%#v", c.when, got, c.want)
			}
		})
	}
}

func TestOpaqueWhenRoundTrips(t *testing.T) {
	const when = "{{= workflow.status == 'Failed' }}"
	o, ok := argo.FromWhen(when, argo.ScopeTask).(expr.Opaque)
	if !ok {
		t.Fatal("not opaque")
	}
	if text, ok := argo.OpaqueWhen(o); !ok || text != when {
		t.Errorf("OpaqueWhen = %q, %v", text, ok)
	}
	if got, ok := argo.ToWhen(o, argo.ScopeTask); !ok || got != when {
		t.Errorf("ToWhen(opaque) = %q, %v; want the text back", got, ok)
	}
	if _, ok := argo.OpaqueWhen(expr.Opaque{Dialect: "tekton", Text: when}); ok {
		t.Error("OpaqueWhen read another dialect")
	}
}

func TestDisabledWhen(t *testing.T) {
	if got := argo.DisabledWhen(); got != "{{= false }}" {
		t.Fatalf("DisabledWhen = %q", got)
	}
	if !argo.IsDisabled(argo.DisabledWhen()) || !argo.IsDisabled("{{=false}}") {
		t.Error("IsDisabled refused the fail-safe")
	}
	for _, w := range []string{"", "{{= true }}", "{{= " + branchRef + " == 'a' }}", "false", "{{= false || true }}", "{{= !true }}"} {
		if argo.IsDisabled(w) {
			t.Errorf("IsDisabled(%q) = true", w)
		}
	}
	// The fail-safe is the printed form of the literal false, so a condition that fails to print can use it.
	if got, ok := argo.ToWhen(boolean(false), argo.ScopeTask); !ok || got != argo.DisabledWhen() {
		t.Errorf("ToWhen(false) = %q, %v", got, ok)
	}
}

// The printed text must not hold a tag end or start that Argo would read before the expression: it
// splits the template text at the first }} and substitutes any {{ it finds.
func TestPrintedTextHasOneTag(t *testing.T) {
	for _, c := range printCases {
		w, _ := argo.ToWhen(c.e, argo.ScopeTask)
		if strings.Count(w, "{{") != 1 || strings.Count(w, "}}") != 1 {
			t.Errorf("%s: %q holds more than one tag mark", c.name, w)
		}
	}
}

func TestSemanticsAreStrict(t *testing.T) {
	if !argo.Semantics.StrictTypes || argo.Semantics.CaseInsensitiveEq {
		t.Errorf("Semantics = %+v; want exact strings with no coercion", argo.Semantics)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"{{workflow.parameters.ctx-branch}} == main", "{{= " + branchRef + " == 'a' && !(" + needsRef + " != 'b') }}",
		"{{= false }}", `{{= ` + branchRef + ` == 'it\'s \\ \n' }}`, "{{ item.os }} != 'x' || ({{tasks.a.outputs.parameters.b}} == c)",
		"{{= " + branchRef + " =~ '^v' }}", "{{=", "{{", "}}", "{{= '", "{{= (((", "{{= ?? }}", "{{= x ?? '' }}",
		"{{= workflow.parameters['a\\x00'] == '\\u00e9' }}",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, when string) {
		for _, scope := range []argo.Scope{argo.ScopeTask, argo.ScopeTemplate} {
			e := argo.FromWhen(when, scope)
			s, ok := argo.ToWhen(e, scope)
			if !ok {
				continue
			}
			back := argo.FromWhen(s, scope)
			if !reflect.DeepEqual(back, e) {
				t.Fatalf("FromWhen(%q) = %#v prints %q and reads back %#v", when, e, s, back)
			}
			if s2, ok := argo.ToWhen(back, scope); !ok || s2 != s {
				t.Fatalf("print not stable: %q then %q", s, s2)
			}
		}
	})
}
