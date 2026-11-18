package expr_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"pgregory.net/rapid"
)

// canonEnv is the dialect-independent environment of the Lean model (Env in Pipemaster/Expr.lean).
type canonEnv struct {
	RefName, RefType, Event, SHA, Nat *string
}

func sp(s string) *string { return &s }

// view is one dialect's name-to-value map over a canonEnv.
type view map[string]string

func (v view) Lookup(p []string) (expr.Value, bool) {
	s, ok := v[strings.Join(p, ".")]
	if !ok {
		return nil, false
	}
	return s, true
}
func (view) Status() expr.Status { return expr.Success }

func put(v view, key string, val *string) {
	if val != nil {
		v[key] = *val
	}
}

// ghaView mirrors ghaView in Pipemaster/Expr.lean.
func (e canonEnv) ghaView() view {
	v := view{}
	put(v, "ref_name", e.RefName)
	put(v, "ref_type", e.RefType)
	put(v, "event", e.Event)
	put(v, "sha", e.SHA)
	put(v, "gha.github.run_id", e.Nat)
	return v
}

// gitlabView mirrors mapEnv in Pipemaster/Expr.lean.
func (e canonEnv) gitlabView() view {
	v := view{}
	put(v, "ref_name", e.RefName)
	if e.Event != nil {
		put(v, "event", sp(gitlab.Events[*e.Event]))
	}
	put(v, "sha", e.SHA)
	if e.RefType != nil && e.RefName != nil {
		put(v, *e.RefType, e.RefName)
	}
	put(v, "gitlab.CI_JOB_NAME", e.Nat)
	return v
}

func ghaParse(t testing.TB, src string) expr.Expr {
	t.Helper()
	e, err := gha.Parse(src)
	if err != nil {
		t.Fatalf("gha.Parse(%q): %v", src, err)
	}
	return e
}

func glParse(t testing.TB, src string) expr.Expr {
	t.Helper()
	e, err := gitlab.Parse(src)
	if err != nil {
		t.Fatalf("gitlab.Parse(%q): %v", src, err)
	}
	return e
}

func toGitLabText(t testing.TB, src string) (string, bool) {
	t.Helper()
	out, ok := expr.Translate(ghaParse(t, src), gha.Lang, gitlab.Lang)
	if !ok {
		return "", false
	}
	s, err := gitlab.Print(out)
	if err != nil {
		t.Fatalf("gitlab.Print(Translate(%q)): %v", src, err)
	}
	return s, true
}

func toGHAText(t testing.TB, src string) (string, bool) {
	t.Helper()
	out, ok := expr.Translate(glParse(t, src), gitlab.Lang, gha.Lang)
	if !ok {
		return "", false
	}
	s, err := gha.Print(out)
	if err != nil {
		t.Fatalf("gha.Print(Translate(%q)): %v", src, err)
	}
	return s, true
}

func TestTranslateBranchEquality(t *testing.T) {
	for src, want := range map[string]string{
		`github.ref_type == 'branch' && github.ref_name == 'main'`:          `$CI_COMMIT_BRANCH =~ /^main$/i`,
		`github.ref_name == 'main' && github.ref_type == 'branch'`:          "",
		`'tag' == github.ref_type && 'v1' == github.ref_name`:               `$CI_COMMIT_TAG =~ /^v1$/i`,
		`github.ref_type == 'BRANCH' && github.ref_name == 'Main'`:          `$CI_COMMIT_BRANCH =~ /^Main$/i`,
		`github.ref_type == 'branch' && startsWith(github.ref_name, 'rel')`: `$CI_COMMIT_BRANCH =~ /^rel/i`,
		`!(github.ref_type == 'branch' && github.ref_name == 'main')`:       `!($CI_COMMIT_BRANCH =~ /^main$/i)`,
		`github.ref_type == 'branch'`:                                       "",
		`github.ref_type == 'branch' && github.ref_name != 'main'`:          "",
		`github.ref_type == 'merge' && github.ref_name == 'main'`:           "",
	} {
		got, ok := toGitLabText(t, src)
		if (want != "") != ok || got != want {
			t.Errorf("Translate(%s) = %q, %v; want %q", src, got, ok, want)
		}
	}
	for src, want := range map[string]string{
		`$CI_COMMIT_BRANCH =~ /^main$/i`: `github.ref_type == 'branch' && github.ref_name == 'main'`,
		`$CI_COMMIT_TAG =~ /^v1$/i`:      `github.ref_type == 'tag' && github.ref_name == 'v1'`,
		`$CI_COMMIT_BRANCH !~ /^main$/i`: `!(github.ref_type == 'branch' && github.ref_name == 'main')`,
		`$CI_COMMIT_BRANCH`:              `github.ref_type == 'branch' && github.ref_name`,
		`$CI_COMMIT_BRANCH =~ /^rel/i`:   `github.ref_type == 'branch' && startsWith(github.ref_name, 'rel')`,
		`$CI_COMMIT_BRANCH == "main"`:    "",
	} {
		got, ok := toGHAText(t, src)
		if (want != "") != ok || got != want {
			t.Errorf("Translate(%s) = %q, %v; want %q", src, got, ok, want)
		}
	}
	// A canonical branch ref has no GHA name, so GHA-side source containing one is outside the subset.
	if _, ok := expr.Translate(expr.Ref{Path: []string{expr.CtxBranch}}, gha.Lang, gitlab.Lang); ok {
		t.Error("bare branch must not translate from GHA")
	}
}

func TestTranslateCaseRule(t *testing.T) {
	// GHA compares strings case-insensitively and GitLab does not, so == on a ref becomes a /i regex.
	for src, want := range map[string]string{
		`github.ref_name == 'Main'`:                   `$CI_COMMIT_REF_NAME =~ /^Main$/i`,
		`'Main' == github.ref_name`:                   `$CI_COMMIT_REF_NAME =~ /^Main$/i`,
		`github.ref_name != 'Main'`:                   `$CI_COMMIT_REF_NAME !~ /^Main$/i`,
		`github.sha == 'abc'`:                         `$CI_COMMIT_SHA =~ /^abc$/i`,
		`github.ref_name == 'release/1.0'`:            `$CI_COMMIT_REF_NAME =~ /^release\/1\.0$/i`,
		`startsWith(github.ref_name, 'rel')`:          `$CI_COMMIT_REF_NAME =~ /^rel/i`,
		`endsWith(github.ref_name, '-rc')`:            `$CI_COMMIT_REF_NAME =~ /-rc$/i`,
		`contains(github.ref_name, 'fix')`:            `$CI_COMMIT_REF_NAME =~ /fix/i`,
		`!startsWith(github.ref_name, 'rel')`:         `!($CI_COMMIT_REF_NAME =~ /^rel/i)`,
		`github.ref_name == github.sha`:               "",
		`github.ref_name == github.ref_name`:          "",
		`'a' == 'a'`:                                  "",
		`github.ref_name == ''`:                       "",
		`github.ref_name == '0'`:                      "",
		`github.ref_name == '-1'`:                     "",
		`github.ref_name == 'é'`:                      "",
		`github.ref_name == 1`:                        "",
		`github.ref_name == null`:                     "",
		`contains(github.ref_name, 'x')`:              "",
		`contains('abc', github.ref_name)`:            "",
		`startsWith(github.ref_name, github.sha)`:     "",
		`github.ref_name < 'b'`:                       "",
		`github.ref_name`:                             `$CI_COMMIT_REF_NAME`,
		`github.ref_name == 'a' && github.sha`:        `$CI_COMMIT_REF_NAME =~ /^a$/i && $CI_COMMIT_SHA`,
		`github.sha && github.ref_name == 'a'`:        "",
		`github.ref_name == 'a' || github.sha == 'b'`: `$CI_COMMIT_REF_NAME =~ /^a$/i || $CI_COMMIT_SHA =~ /^b$/i`,
	} {
		got, ok := toGitLabText(t, src)
		if (want != "") != ok || got != want {
			t.Errorf("Translate(%s) = %q, %v; want %q", src, got, ok, want)
		}
	}
	for src, want := range map[string]string{
		`$CI_COMMIT_REF_NAME =~ /^Main$/i`:                `github.ref_name == 'Main'`,
		`$CI_COMMIT_REF_NAME !~ /^Main$/i`:                `!(github.ref_name == 'Main')`,
		`$CI_COMMIT_REF_NAME =~ /^rel/i`:                  `startsWith(github.ref_name, 'rel')`,
		`$CI_COMMIT_REF_NAME =~ /-rc$/i`:                  `endsWith(github.ref_name, '-rc')`,
		`$CI_COMMIT_REF_NAME =~ /fix/i`:                   `contains(github.ref_name, 'fix')`,
		`$CI_COMMIT_REF_NAME =~ /^a\/b\.c$/i`:             `github.ref_name == 'a/b.c'`,
		`$CI_COMMIT_REF_NAME =~ /^main$/`:                 "",
		`$CI_COMMIT_REF_NAME =~ /^ma.n$/i`:                "",
		`$CI_COMMIT_REF_NAME =~ /^0$/i`:                   "",
		`$CI_COMMIT_REF_NAME == "main"`:                   "",
		`$CI_COMMIT_REF_NAME != "main"`:                   "",
		`$CI_COMMIT_REF_NAME == null`:                     "",
		`$CI_COMMIT_REF_NAME == ""`:                       "",
		`$CI_COMMIT_REF_NAME`:                             `github.ref_name`,
		`$CI_COMMIT_REF_NAME && $CI_COMMIT_SHA`:           "",
		`$CI_COMMIT_REF_NAME =~ /^a$/i && $CI_COMMIT_SHA`: `github.ref_name == 'a' && github.sha`,
		`$CI_JOB_NAME =~ /^a$/i`:                          "",
		`$CI_COMMIT_REF_NAME =~ /ab/`:                     "",
	} {
		got, ok := toGHAText(t, src)
		if (want != "") != ok || got != want {
			t.Errorf("Translate(%s) = %q, %v; want %q", src, got, ok, want)
		}
	}
}

func TestTranslateEventLiteral(t *testing.T) {
	for src, want := range map[string]string{
		`github.event_name == 'push'`:              `$CI_PIPELINE_SOURCE =~ /^push$/i`,
		`github.event_name == 'pull_request'`:      `$CI_PIPELINE_SOURCE =~ /^merge_request_event$/i`,
		`github.event_name == 'PULL_REQUEST'`:      `$CI_PIPELINE_SOURCE =~ /^merge_request_event$/i`,
		`github.event_name == 'schedule'`:          `$CI_PIPELINE_SOURCE =~ /^schedule$/i`,
		`github.event_name != 'workflow_dispatch'`: `$CI_PIPELINE_SOURCE !~ /^web$/i`,
		`'push' == github.event_name`:              `$CI_PIPELINE_SOURCE =~ /^push$/i`,
		`github.event_name == 'release'`:           "",
		`github.event_name == 'web'`:               "",
		`github.event_name == github.ref_name`:     "",
		`startsWith(github.event_name, 'push')`:    "",
		`github.ref_name == 'pull_request'`:        `$CI_COMMIT_REF_NAME =~ /^pull_request$/i`,
	} {
		got, ok := toGitLabText(t, src)
		if (want != "") != ok || got != want {
			t.Errorf("Translate(%s) = %q, %v; want %q", src, got, ok, want)
		}
	}
	for src, want := range map[string]string{
		`$CI_PIPELINE_SOURCE =~ /^merge_request_event$/i`: `github.event_name == 'pull_request'`,
		`$CI_PIPELINE_SOURCE =~ /^web$/i`:                 `github.event_name == 'workflow_dispatch'`,
		`$CI_PIPELINE_SOURCE !~ /^push$/i`:                `!(github.event_name == 'push')`,
		`$CI_PIPELINE_SOURCE =~ /^pull_request$/i`:        "",
		`$CI_PIPELINE_SOURCE =~ /^api$/i`:                 "",
		`$CI_PIPELINE_SOURCE =~ /^push/i`:                 "",
	} {
		got, ok := toGHAText(t, src)
		if (want != "") != ok || got != want {
			t.Errorf("Translate(%s) = %q, %v; want %q", src, got, ok, want)
		}
	}
}

func TestUntranslatableHashFiles(t *testing.T) {
	for _, src := range []string{
		`hashFiles('**/go.sum') == 'x'`, `success()`, `failure() && github.ref_name == 'a'`, `always()`, `cancelled()`,
		`format('{0}', github.ref_name) == 'a'`, `join(github.ref_name, ',') == 'a'`, `toJSON(github.ref_name) == 'a'`,
		`fromJSON('1') == 1`, `github.ref_name == 1`, `1`, `'a'`, `null`, `true`,
		`github.run_id == 'x'`, `github.event.pull_request.number == '1'`, `github.ref == 'refs/heads/main'`,
		`github.ref_type == 'branch'`, `github.head_ref == 'x'`, `matrix.os == 'linux'`, `secrets.x`,
		`github.ref_name == 'a' && failure()`,
	} {
		if s, ok := toGitLabText(t, src); ok {
			t.Errorf("Translate(%s) = %q, want untranslatable", src, s)
		}
		if expr.Translatable(ghaParse(t, src), gha.Lang, gitlab.Lang) {
			t.Errorf("Translatable(%s) = true", src)
		}
	}
	// Provider-native refs of the other provider, Opaque, unknown functions.
	for name, e := range map[string]expr.Expr{
		"foreign native": expr.Ref{Path: []string{"gitlab", "CI_JOB_NAME"}},
		"opaque":         expr.Opaque{Dialect: "gha", Text: "a[b]"},
		"unknown call":   expr.Call{Name: "nope", Args: nil},
	} {
		if _, ok := expr.Translate(e, gha.Lang, gitlab.Lang); ok {
			t.Errorf("%s translated", name)
		}
	}
	// An unsupported pair never translates.
	if _, ok := expr.Translate(expr.Ref{Path: []string{"sha"}}, gha.Lang, gha.Lang); ok {
		t.Error("gha to gha must not translate")
	}
	if _, ok := expr.Translate(expr.Ref{Path: []string{"sha"}}, gitlab.Lang, expr.Dialect{ID: "drone"}); ok {
		t.Error("gitlab to drone must not translate")
	}
	if expr.Translatable(expr.Ref{Path: []string{"sha"}}, gha.Lang, gha.Lang) {
		t.Error("Translatable(gha, gha) must be false")
	}
	for _, src := range []string{`$CI_JOB_NAME`, `$CI_COMMIT_SHA == null`, `$X =~ /^a$/i`, `$CI_PIPELINE_SOURCE =~ /ab/`} {
		if s, ok := toGHAText(t, src); ok {
			t.Errorf("Translate(%s) = %q, want untranslatable", src, s)
		}
	}
}

func TestGitLabLogicalOperandMustBeBoolean(t *testing.T) {
	// GitLab tests && / || operands with Ruby truthiness ("" is truthy); GHA treats "" as falsy.
	if _, ok := toGitLabText(t, `github.sha && github.ref_name == 'a'`); ok {
		t.Error("bare ref on the left of && must not translate")
	}
	if _, ok := toGitLabText(t, `github.sha || github.ref_name == 'a'`); ok {
		t.Error("bare ref on the left of || must not translate")
	}
	if _, ok := toGHAText(t, `$CI_COMMIT_SHA && $CI_COMMIT_REF_NAME =~ /^a$/i`); ok {
		t.Error("bare variable on the left of && must not translate")
	}
	if _, ok := toGitLabText(t, `(github.ref_name == 'a' && github.sha) && github.sha == 'b'`); ok {
		t.Error("an && ending in a bare ref is not boolean-valued")
	}
}

func TestTranslateApprox(t *testing.T) {
	for src, want := range map[string]string{
		`$CI_COMMIT_REF_NAME == "main"`:                     `github.ref_name == 'main'`,
		`$CI_COMMIT_REF_NAME != "main"`:                     `github.ref_name != 'main'`,
		`$CI_COMMIT_BRANCH == "main"`:                       `github.ref_type == 'branch' && github.ref_name == 'main'`,
		`$CI_COMMIT_BRANCH != "main"`:                       `!(github.ref_type == 'branch' && github.ref_name == 'main')`,
		`$CI_PIPELINE_SOURCE == "merge_request_event"`:      `github.event_name == 'pull_request'`,
		`$CI_COMMIT_SHA == "abc" && $CI_COMMIT_TAG == "v1"`: `github.sha == 'abc' && (github.ref_type == 'tag' && github.ref_name == 'v1')`,
		`$CI_COMMIT_SHA == $CI_COMMIT_REF_NAME`:             "",
		`$CI_COMMIT_SHA == null`:                            "",
		`$CI_JOB_NAME == "x"`:                               "",
		`$CI_PIPELINE_SOURCE == "api"`:                      "",
		`$CI_COMMIT_SHA && $CI_COMMIT_SHA == "a"`:           "",
	} {
		out, caveats, ok := expr.TranslateApprox(glParse(t, src), gitlab.Lang, gha.Lang)
		if (want != "") != ok {
			t.Errorf("TranslateApprox(%s) ok = %v, want %v", src, ok, want != "")
			continue
		}
		if !ok {
			continue
		}
		got, err := gha.Print(out)
		if err != nil || got != want {
			t.Errorf("TranslateApprox(%s) = %q, %v; want %q", src, got, err, want)
		}
		if !reflect.DeepEqual(caveats, []string{"GitHub compares strings case-insensitively"}) {
			t.Errorf("TranslateApprox(%s) caveats = %v", src, caveats)
		}
	}
	// Exact translations carry no caveat; GHA to GitLab never approximates.
	out, caveats, ok := expr.TranslateApprox(glParse(t, `$CI_COMMIT_REF_NAME =~ /^a$/i`), gitlab.Lang, gha.Lang)
	if !ok || len(caveats) != 0 || !reflect.DeepEqual(out, ghaParse(t, `github.ref_name == 'a'`)) {
		t.Errorf("exact case: %v %v %v", out, caveats, ok)
	}
	if _, _, ok := expr.TranslateApprox(ghaParse(t, `github.ref_name == github.sha`), gha.Lang, gitlab.Lang); ok {
		t.Error("gha to gitlab must not approximate")
	}
}

func TestPatternRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.StringMatching(`[ -~]{1,8}`).Draw(t, "s")
		m := rapid.SampledFrom([]expr.Mode{expr.ModeExact, expr.ModePrefix, expr.ModeSuffix, expr.ModeSub}).Draw(t, "m")
		gm, gs, ok := expr.ParsePattern(expr.Pattern(m, s))
		if !ok || gm != m || gs != s {
			t.Fatalf("ParsePattern(Pattern(%d, %q)) = %d, %q, %v", m, s, gm, gs, ok)
		}
	})
	for _, p := range []string{"^a", "(?i)", "(?i)^$", "(?i)a.b", "(?i)^a^b", "(?i)a$b", "(?i)(a)", "(?s)a", `(?i)a\`} {
		if _, _, ok := expr.ParsePattern(p); ok {
			t.Errorf("ParsePattern(%q) accepted", p)
		}
	}
}

func genLeaf(t *rapid.T) expr.Expr {
	refs := [][]string{{"ref_name"}, {"sha"}, {"event"}, {"ref_type"}, {"gha", "github", "run_id"}, {"branch"}, {"tag"}, {"gitlab", "CI_JOB_NAME"}}
	lits := []string{"main", "MAIN", "", "push", "pull_request", "merge_request_event", "web", "branch", "tag", "0", "a", "ab"}
	if rapid.Bool().Draw(t, "isref") {
		return expr.Ref{Path: rapid.SampledFrom(refs).Draw(t, "ref")}
	}
	return expr.Lit{Kind: expr.LitStr, Str: rapid.SampledFrom(lits).Draw(t, "lit")}
}

func genNode(t *rapid.T, depth int, gl bool) expr.Expr {
	if depth == 0 {
		return genLeaf(t)
	}
	sub := func() expr.Expr { return genNode(t, depth-1, gl) }
	switch rapid.IntRange(0, 6).Draw(t, "k") {
	case 0:
		return genLeaf(t)
	case 1:
		return expr.Unary{Op: expr.OpNot, X: sub()}
	case 2:
		return expr.Binary{Op: rapid.SampledFrom([]string{expr.OpAnd, expr.OpOr}).Draw(t, "j"), L: sub(), R: sub()}
	case 3:
		return expr.Binary{Op: rapid.SampledFrom([]string{expr.OpEq, expr.OpNe}).Draw(t, "c"), L: genLeaf(t), R: genLeaf(t)}
	case 4:
		return expr.Call{Name: rapid.SampledFrom([]string{"startsWith", "endsWith", "contains"}).Draw(t, "f"), Args: []expr.Expr{genLeaf(t), genLeaf(t)}}
	case 5:
		if !gl {
			return expr.Binary{Op: expr.OpAnd, L: expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"ref_type"}}, R: expr.Lit{Kind: expr.LitStr, Str: rapid.SampledFrom([]string{"branch", "TAG"}).Draw(t, "ty")}}, R: sub()}
		}
		fallthrough
	default:
		return expr.Binary{Op: rapid.SampledFrom([]string{expr.OpMatch, expr.OpNoMatch}).Draw(t, "m"), L: genLeaf(t),
			R: expr.Lit{Kind: expr.LitStr, Str: expr.Pattern(rapid.SampledFrom([]expr.Mode{expr.ModeExact, expr.ModePrefix, expr.ModeSuffix, expr.ModeSub}).Draw(t, "pm"), rapid.SampledFrom([]string{"main", "MAIN", "push", "web", "a", "0"}).Draw(t, "ps"))}}
	}
}

func genCanonEnv(t *rapid.T) canonEnv {
	opt := func(label string, vals ...string) *string {
		v := rapid.SampledFrom(append([]string{"\x00"}, vals...)).Draw(t, label)
		if v == "\x00" {
			return nil
		}
		return &v
	}
	return canonEnv{
		RefName: opt("rn", "main", "MAIN", "Main", "", "v1", "a"), RefType: opt("rt", "branch", "tag"),
		Event: opt("ev", "push", "pull_request", "schedule", "workflow_dispatch"), SHA: opt("sha", "abc", "ABC", ""), Nat: opt("nat", "x"),
	}
}

// TestTranslateRandomAgreement extends the vectors to deeper random expressions: Translatable always
// matches Translate, and a translation keeps the truthiness on random environments.
func TestTranslateRandomAgreement(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		fwd := rapid.Bool().Draw(t, "fwd")
		from, to := gha.Lang, gitlab.Lang
		sv, tv := canonEnv.ghaView, canonEnv.gitlabView
		if !fwd {
			from, to, sv, tv = gitlab.Lang, gha.Lang, tv, sv
		}
		e := genNode(t, 3, !fwd)
		out, ok := expr.Translate(e, from, to)
		if expr.Translatable(e, from, to) != ok {
			t.Fatalf("Translatable != Translate for %s", expr.String(e))
		}
		if !ok {
			return
		}
		env := genCanonEnv(t)
		if a, b := holdsOf(t, e, sv(env), from.Sem), holdsOf(t, out, tv(env), to.Sem); a != b {
			t.Fatalf("%s = %v but %s = %v", expr.String(e), a, expr.String(out), b)
		}
	})
}
