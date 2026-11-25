package woodpecker_test

// Rules pinned here come from the expr-lang v1.17.8 parser (parser/operator: or 10, and 15, comparisons 20,
// not 50, left associative) and from the Woodpecker v3.20.1 evaluator (pipeline/frontend/yaml/constraint),
// which compiles with a map[string]string environment: an unset name reads "".

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"pgregory.net/rapid"
)

func r(p ...string) expr.Expr                 { return expr.Ref{Path: p} }
func bin(op string, l, x expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: x} }
func str(s string) expr.Expr                  { return expr.Lit{Kind: expr.LitStr, Str: s} }
func not(x expr.Expr) expr.Expr               { return expr.Unary{Op: expr.OpNot, X: x} }
func call(n string, a ...expr.Expr) expr.Expr { return expr.Call{Name: n, Args: a} }

var (
	null   = expr.Lit{}
	branch = r("branch")
)

func TestParsePrintTable(t *testing.T) {
	cases := []struct {
		src  string
		want expr.Expr
		out  string // printed form when it differs from src
	}{
		{`CI_COMMIT_BRANCH == "main"`, bin(expr.OpEq, branch, str("main")), ""},
		{`CI_COMMIT_BRANCH == 'main'`, bin(expr.OpEq, branch, str("main")), `CI_COMMIT_BRANCH == "main"`},
		{`CI_COMMIT_TAG != ""`, bin(expr.OpNe, r("tag"), str("")), ""},
		{`CI_COMMIT_SHA`, r("sha"), ""},
		{`CI_PIPELINE_EVENT == "pull_request"`, bin(expr.OpEq, r("event"), str("pull_request")), ""},
		{`CI_REPO == "a/b"`, bin(expr.OpEq, r("repo"), str("a/b")), ""},
		{`CI_COMMIT_AUTHOR == "bob"`, bin(expr.OpEq, r("actor"), str("bob")), ""},
		{`CI_COMMIT_PULL_REQUEST`, r("pr", "number"), ""},
		{`CI_COMMIT_REF == "refs/heads/main"`, bin(expr.OpEq, r("ref"), str("refs/heads/main")), ""},
		{`CI_COMMIT_SOURCE_BRANCH`, r("pr", "head_ref"), ""},
		{`CI_COMMIT_TARGET_BRANCH`, r("pr", "base_ref"), ""},
		{`CI_REPO_DEFAULT_BRANCH`, r("default_branch"), ""},
		{`SKIP != "true"`, bin(expr.OpNe, r("env", "SKIP"), str("true")), ""},
		{`DEPLOY_KEY != nil`, bin(expr.OpNe, r("env", "DEPLOY_KEY"), null), ""},
		{`CI_COMMIT_BRANCH == CI_REPO_DEFAULT_BRANCH`, bin(expr.OpEq, branch, r("default_branch")), ""},
		{`CI_COMMIT_BRANCH matches "^release-"`, bin(expr.OpMatch, branch, str("^release-")), ""},
		{`CI_COMMIT_BRANCH matches "^v\\d+$"`, bin(expr.OpMatch, branch, str(`^v\d+$`)), ""},
		{`CI_COMMIT_MESSAGE contains "ci skip"`, call("contains", r("woodpecker", "CI_COMMIT_MESSAGE"), str("ci skip")), ""},
		{`CI_COMMIT_BRANCH startsWith "feat/"`, call("startsWith", branch, str("feat/")), ""},
		{`CI_COMMIT_BRANCH endsWith "-wip"`, call("endsWith", branch, str("-wip")), ""},
		{`CI_COMMIT_BRANCH and A or B`, bin(expr.OpOr, bin(expr.OpAnd, branch, r("env", "A")), r("env", "B")), `CI_COMMIT_BRANCH && A || B`},
		{`A || B && C`, bin(expr.OpOr, r("env", "A"), bin(expr.OpAnd, r("env", "B"), r("env", "C"))), ""},
		{`(A || B) && C`, bin(expr.OpAnd, bin(expr.OpOr, r("env", "A"), r("env", "B")), r("env", "C")), ""},
		{`not (CI_COMMIT_MESSAGE contains "x")`, not(call("contains", r("woodpecker", "CI_COMMIT_MESSAGE"), str("x"))), `!(CI_COMMIT_MESSAGE contains "x")`},
		{`!A`, not(r("env", "A")), ""},
		{`!A == "x"`, bin(expr.OpEq, not(r("env", "A")), str("x")), ""},
		{`A == true`, bin(expr.OpEq, r("env", "A"), expr.Lit{Kind: expr.LitBool, Bool: true}), ""},
		{`A == "say \"hi\"\n"`, bin(expr.OpEq, r("env", "A"), str("say \"hi\"\n")), ""},
		{`A == 'it\'s'`, bin(expr.OpEq, r("env", "A"), str("it's")), `A == "it's"`},
		{`((A))`, r("env", "A"), `A`},
		// `in` has no AST node: it is the OR of equalities expr-lang evaluates it as.
		{`CI_PIPELINE_EVENT in ["push", "tag"]`, bin(expr.OpOr, bin(expr.OpEq, r("event"), str("push")), bin(expr.OpEq, r("event"), str("tag"))),
			`CI_PIPELINE_EVENT == "push" || CI_PIPELINE_EVENT == "tag"`},
	}
	for _, c := range cases {
		got, err := woodpecker.Parse(c.src)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.src, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %#v, want %#v", c.src, got, c.want)
			continue
		}
		want := c.out
		if want == "" {
			want = c.src
		}
		out, err := woodpecker.Print(got)
		if err != nil || out != want {
			t.Errorf("Print(Parse(%q)) = %q, %v; want %q", c.src, out, err, want)
		}
	}
}

func TestOpaqueForUnmodelledSyntax(t *testing.T) {
	for _, src := range []string{
		`CI_COMMIT_BRANCH matches A`, `A in $env`, `$env.A == "x"`, `not("X" in $env)`, `len(A) > 3`, `A == 1`,
		`A ? B : C`, "A == `x`", `A not in ["x"]`, `A in []`, `A in ["x", B]`, `A.b == "c"`, `A == "\x41"`,
		`A + B`, `CI_COMMIT_BRANCH == "a\u00e9"`,
	} {
		got, err := woodpecker.Parse(src)
		if err != nil {
			t.Errorf("Parse(%q): %v", src, err)
			continue
		}
		if o, ok := got.(expr.Opaque); !ok || o.Dialect != "woodpecker" || o.Text != src {
			t.Errorf("Parse(%q) = %#v, want woodpecker Opaque", src, got)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for src, why := range map[string]string{
		``:                       "empty",
		`A ==`:                   "missing operand",
		`A == "x`:                "unterminated string",
		"A == `x":                "unterminated raw string",
		`A == "x\`:               "dangling escape",
		`(A`:                     "unmatched paren",
		`A)`:                     "stray paren",
		`A B`:                    "adjacent operands",
		`A == "x" "y"`:           "trailing token",
		`A matches "("`:          "invalid RE2",
		`when.branch glob "a*"`:  "reserved glob text",
		`and A`:                  "operator first",
		strings.Repeat("(", 60):  "nesting too deep",
		strings.Repeat("!", 100): "unary nesting too deep",
	} {
		if _, err := woodpecker.Parse(src); err == nil {
			t.Errorf("Parse(%q) succeeded, want error (%s)", src, why)
		}
	}
}

func TestPrintErrors(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"number":          expr.Lit{Kind: expr.LitNum, Num: 1},
		"unknown call":    call("format", str("x")),
		"call arity":      call("contains", str("x")),
		"status call":     call("success"),
		"ordering":        bin(expr.OpLt, r("sha"), str("a")),
		"ref_type":        r("ref_type"),
		"gha native":      r("gha", "run_id"),
		"env keyword":     r("env", "not"),
		"env CI prefix":   r("env", "CI_COMMIT_BRANCH"),
		"native mapped":   r("woodpecker", "CI_COMMIT_BRANCH"),
		"native no CI_":   r("woodpecker", "FOO"),
		"bad env name":    r("env", "a b"),
		"control char":    bin(expr.OpEq, r("sha"), str("a\x01")),
		"invalid utf8":    bin(expr.OpEq, r("sha"), str("a\xff")),
		"regex not lit":   bin(expr.OpMatch, r("sha"), r("sha")),
		"bad regex":       bin(expr.OpMatch, r("sha"), str("(")),
		"foreign opaque":  expr.Opaque{Dialect: "gha", Text: "x"},
		"glob opaque":     woodpecker.GlobOpaque("branch", "release/*"),
		"glob in a chain": bin(expr.OpOr, bin(expr.OpEq, branch, str("a")), woodpecker.GlobOpaque("branch", "r/*")),
	} {
		if s, err := woodpecker.Print(e); err == nil {
			t.Errorf("%s: Print = %q, want error", name, s)
		}
	}
}

func TestPrintParensAndPatterns(t *testing.T) {
	e := bin(expr.OpAnd, bin(expr.OpOr, r("env", "A"), r("env", "B")), bin(expr.OpNoMatch, r("sha"), str("a/b")))
	got, err := woodpecker.Print(e)
	if want := `(A || B) && !(CI_COMMIT_SHA matches "a/b")`; err != nil || got != want {
		t.Errorf("Print = %q, %v; want %q", got, err, want)
	}
	// Left associativity: a right operand at the same level needs its parentheses.
	e = bin(expr.OpEq, bin(expr.OpEq, r("env", "A"), str("x")), bin(expr.OpEq, r("env", "B"), str("y")))
	if got, err := woodpecker.Print(e); err != nil || got != `A == "x" == (B == "y")` {
		t.Errorf("Print = %q, %v", got, err)
	}
}

type env map[string]expr.Value

func (e env) Lookup(p []string) (expr.Value, bool) { v, ok := e[strings.Join(p, ".")]; return v, ok }
func (env) Status() expr.Status                    { return expr.Success }

func truth(t *testing.T, src string, e env) bool {
	t.Helper()
	x, err := woodpecker.Parse(src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	v, err := expr.Eval(x, woodpecker.UnsetEnv{Env: e}, woodpecker.Semantics)
	if err != nil {
		t.Fatalf("Eval(%q): %v", src, err)
	}
	return v == true
}

// Pinned against expr-lang v1.17.8 with a map[string]string env (scratch probe, 2026-11-20):
// UNDEF == "" true, UNDEF != nil true, UNDEF == nil false, UNDEF matches "a" false.
func TestUnsetReadsAsEmptyString(t *testing.T) {
	unset, empty, set := env{}, env{"env.A": ""}, env{"env.A": "Main"}
	for _, c := range []struct {
		src                 string
		unset, empty, isSet bool
	}{
		{`A == ""`, true, true, false},
		{`A != ""`, false, false, true},
		{`A == nil`, false, false, false},
		{`A != nil`, true, true, true},
		{`A == "main"`, false, false, false},
		{`A == "Main"`, false, false, true},
		{`A matches "^main$"`, false, false, false},
		{`A matches "(?i)^main$"`, false, false, true},
		{`A startsWith "M"`, false, false, true},
		{`A contains ""`, true, true, true},
		{`A in ["Main", "x"]`, false, false, true},
		{`A in ["", "x"]`, true, true, false},
		{`not (A == "x")`, true, true, true},
	} {
		for name, got := range map[string]bool{"unset": truth(t, c.src, unset), "empty": truth(t, c.src, empty), "set": truth(t, c.src, set)} {
			want := map[string]bool{"unset": c.unset, "empty": c.empty, "set": c.isSet}[name]
			if got != want {
				t.Errorf("%s with %s variable = %v, want %v", c.src, name, got, want)
			}
		}
	}
}

func TestAndBindsTighterThanOr(t *testing.T) {
	e := env{"env.A": "1", "env.B": "", "env.C": ""}
	if !truth(t, `A == "1" or B == "x" and C == "x"`, e) {
		t.Error("and must bind tighter than or")
	}
}

func TestEventMap(t *testing.T) {
	want := map[string]string{"pull_request": "pull_request", "schedule": "cron", "workflow_dispatch": "manual"}
	if !reflect.DeepEqual(woodpecker.Events, want) {
		t.Errorf("Events = %v", woodpecker.Events)
	}
}

// The whole-class claim is refused (lean4/Pipemaster/Woodpecker.lean: not_gitlab_class); only the proved subset
// shares the GitLab class (eval_woodpecker), so Reads and Supports must reject what lies outside it.
func TestLangConfinesTranslationToTheSubset(t *testing.T) {
	if woodpecker.Lang.Class != expr.ClassGitLab {
		t.Fatalf("class = %d", woodpecker.Lang.Class)
	}
	in := bin(expr.OpMatch, branch, str("(?i)^main$"))
	for name, e := range map[string]expr.Expr{
		"eq empty":     bin(expr.OpEq, r("tag"), str("")),
		"ne nil":       bin(expr.OpNe, r("tag"), null),
		"eq event":     bin(expr.OpEq, r("event"), str("pull_request")),
		"eq on right":  bin(expr.OpEq, str("main"), branch),
		"not ref":      not(branch),
		"call":         call("contains", branch, str("a")),
		"raw pattern":  bin(expr.OpMatch, branch, str(`^v\d+`)),
		"and bare ref": bin(expr.OpAnd, in, branch),
		"or bare ref":  bin(expr.OpOr, branch, in),
	} {
		if woodpecker.Lang.Reads(e) || woodpecker.Lang.Supports(e) {
			t.Errorf("%s: outside the subset but accepted", name)
		}
	}
	for name, e := range map[string]expr.Expr{
		"match": in, "no match": bin(expr.OpNoMatch, branch, str("(?i)abc")),
		"not": not(in), "and or": bin(expr.OpOr, bin(expr.OpAnd, in, not(in)), in),
		"eq": bin(expr.OpEq, branch, str("main")), "ne": bin(expr.OpNe, r("tag"), str("v1")),
		"or eq": bin(expr.OpOr, in, bin(expr.OpEq, branch, str("x"))),
	} {
		if !woodpecker.Lang.Reads(e) || !woodpecker.Lang.Supports(e) {
			t.Errorf("%s: inside the subset but refused", name)
		}
	}
	// The IR holds ref != "" as a bare ref, so the dialect reads and supports the bare form and writes the comparison.
	// A ref != "" in the IR is the GitLab test, true for an unset ref, so it is neither read nor supported.
	nonEmpty := bin(expr.OpNe, branch, str(""))
	if woodpecker.Lang.Reads(nonEmpty) || woodpecker.Lang.Supports(nonEmpty) || !woodpecker.Lang.Reads(branch) || !woodpecker.Lang.Supports(branch) {
		t.Error("a bare ref must be read and supported, ref != \"\" neither")
	}
	if got := woodpecker.ReadIf(nonEmpty); !reflect.DeepEqual(got, branch) {
		t.Errorf("ReadIf(ref != \"\") = %#v, want the bare ref", got)
	}
	if got := woodpecker.ReadIf(branch); !reflect.DeepEqual(got, expr.Opaque{Dialect: woodpecker.Dialect, Text: "CI_COMMIT_BRANCH"}) {
		t.Errorf("ReadIf(bare name) = %#v, want opaque text", got)
	}
	if got, ok := expr.LangOf("woodpecker"); !ok || got.ID != "woodpecker" {
		t.Errorf("LangOf(woodpecker) = %v, %v", got, ok)
	}
}

func genStr(t *rapid.T, label string) string {
	return rapid.StringMatching(`[A-Za-z0-9_/ .'"\\\n-]{0,6}`).Draw(t, label)
}

func genNode(t *rapid.T, depth int) expr.Expr {
	k := rapid.IntRange(0, 7).Draw(t, "k")
	if depth <= 0 {
		k %= 3
	}
	switch k {
	case 0:
		return expr.Ref{Path: rapid.SampledFrom([][]string{{"branch"}, {"tag"}, {"sha"}, {"event"}, {"ref"}, {"pr", "number"}, {"woodpecker", "CI_REPO_URL"}, {"env", "x1"}}).Draw(t, "ref")}
	case 1:
		return expr.Lit{Kind: expr.LitStr, Str: genStr(t, "s")}
	case 2:
		return rapid.SampledFrom([]expr.Expr{null, expr.Lit{Kind: expr.LitBool, Bool: true}, expr.Lit{Kind: expr.LitBool}}).Draw(t, "lit")
	case 3:
		return expr.Unary{Op: expr.OpNot, X: genNode(t, depth-1)}
	case 4, 5:
		return expr.Binary{Op: rapid.SampledFrom([]string{expr.OpEq, expr.OpNe, expr.OpAnd, expr.OpOr}).Draw(t, "op"), L: genNode(t, depth-1), R: genNode(t, depth-1)}
	case 6:
		return expr.Call{Name: rapid.SampledFrom([]string{"contains", "startsWith", "endsWith"}).Draw(t, "fn"), Args: []expr.Expr{genNode(t, depth-1), genNode(t, depth-1)}}
	}
	pat := rapid.StringMatching(`[a-z/^$.*]{1,6}`).Draw(t, "pat")
	return expr.Binary{Op: expr.OpMatch, L: genNode(t, depth-1), R: expr.Lit{Kind: expr.LitStr, Str: pat}}
}

func TestPrintParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := genNode(t, 4)
		s, err := woodpecker.Print(e)
		if err != nil {
			t.Skip()
		}
		got, err := woodpecker.Parse(s)
		if err != nil || !reflect.DeepEqual(got, e) {
			t.Fatalf("Parse(Print(%#v)) = %#v, %v (text %q)", e, got, err, s)
		}
	})
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`CI_COMMIT_BRANCH == "main"`, `A in ["x", "y"] && !B`, `(A || B) and C != nil`, `A matches "^v\\d"`,
		`not("X" in $env)`, `A contains "x" or B startsWith 'y'`, `A == "\n\t\\"`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		e, err := woodpecker.Parse(src)
		if err != nil {
			return
		}
		s, err := woodpecker.Print(e)
		if err != nil {
			return
		}
		back, err := woodpecker.Parse(s)
		if err != nil {
			t.Fatalf("Print(%#v) = %q does not reparse: %v", e, s, err)
		}
		s2, err := woodpecker.Print(back)
		if err != nil || s2 != s {
			t.Fatalf("print not stable: %q then %q (%v)", s, s2, err)
		}
	})
}

func TestParseDepthBoundary(t *testing.T) {
	m := expr.MaxDepth
	for _, c := range []struct {
		name string
		src  string
		ok   bool
	}{
		{"parens at max", strings.Repeat("(", m) + "A" + strings.Repeat(")", m), true},
		{"parens over", strings.Repeat("(", m+1) + "A" + strings.Repeat(")", m+1), false},
		{"unary at max", strings.Repeat("!", m) + "A", true},
		{"unary over", strings.Repeat("!", m+1) + "A", false},
	} {
		if _, err := woodpecker.Parse(c.src); (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestLongOperatorChainsAreCheckValid(t *testing.T) {
	for name, src := range map[string]string{
		"flat":  strings.Repeat("A || ", 200) + "A",
		"mixed": strings.Repeat(`A == "x" || B && C != "y" || `, 200) + "A",
	} {
		e, err := woodpecker.Parse(src)
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
