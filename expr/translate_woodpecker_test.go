package expr_test

// The Woodpecker dialect shares the GitLab class on the subset lean4/Pipemaster/Woodpecker.lean proves
// (eval_woodpecker, woodpecker_event_rx, woodpecker_bare): regex and equality tests of a ref against a non-empty
// literal under !, && and ||, and a bare ref as the whole condition.
// These tests run the Lean vectors of the gha-gitlab and gitlab-gha directions through Woodpecker.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
	"pgregory.net/rapid"
)

// wpEvents names each canonical event as CI_PIPELINE_EVENT reports it, push included: the dialect refuses to
// translate a push literal, but a push run still exists.
var wpEvents = map[string]string{"push": "push", "pull_request": "pull_request", "schedule": "cron", "workflow_dispatch": "manual"}

// woodpeckerView is the canonical-keyed environment a Woodpecker run presents; the evaluator reads a name it
// lacks as "" (woodpecker.UnsetEnv).
func (e canonEnv) woodpeckerView() expr.Env {
	v := e.gitlabView()
	delete(v, "event")
	if e.Event != nil {
		v["event"] = wpEvents[*e.Event]
	}
	return woodpecker.UnsetEnv{Env: v}
}

// reprint reads text back and prints it. `!~` has no expr-lang token, so it comes back as `!(x matches p)`.
func reprint(text string) (string, error) {
	back, err := woodpecker.Parse(text)
	if err != nil {
		return "", err
	}
	return woodpecker.Print(back)
}

func woodpeckerHolds(t fataler, e expr.Expr, v expr.Env) bool {
	t.Helper()
	val, err := expr.Eval(e, v, woodpecker.Semantics)
	if err != nil {
		t.Fatalf("Eval(%s): %v", expr.String(e), err)
	}
	b, _ := val.(bool)
	return b
}

// inSubset restates the proved subset for a gitlab-class expression over canonical refs, independently of
// the Go predicate: regex tests of branch, tag, sha or event, and equality tests of branch, tag or sha, combined
// with !, && and ||, or a bare branch, tag or sha. Event tests are exact and name a gitlab event value that has a
// Woodpecker counterpart other than push.
func inSubset(e expr.Expr) bool {
	if r, ok := e.(expr.Ref); ok {
		return len(r.Path) == 1 && (r.Path[0] == "branch" || r.Path[0] == "tag" || r.Path[0] == "sha")
	}
	return inSubsetTree(e)
}

func inSubsetTree(e expr.Expr) bool {
	switch v := e.(type) {
	case expr.Unary:
		return inSubsetTree(v.X)
	case expr.Binary:
		switch v.Op {
		case expr.OpAnd, expr.OpOr:
			return inSubsetTree(v.L) && inSubsetTree(v.R)
		case expr.OpMatch, expr.OpNoMatch:
			return rxInSubset(v)
		case expr.OpEq, expr.OpNe:
			return eqInSubset(v)
		}
	}
	return false
}

func eqInSubset(b expr.Binary) bool {
	r, ok := b.L.(expr.Ref)
	lit, isLit := b.R.(expr.Lit)
	return ok && isLit && lit.Kind == expr.LitStr && lit.Str != "" && len(r.Path) == 1 &&
		(r.Path[0] == "branch" || r.Path[0] == "tag" || r.Path[0] == "sha")
}

func rxInSubset(b expr.Binary) bool {
	r, ok := b.L.(expr.Ref)
	lit, isLit := b.R.(expr.Lit)
	if !ok || !isLit || lit.Kind != expr.LitStr || len(r.Path) != 1 {
		return false
	}
	mode, s, pok := expr.ParsePattern(lit.Str)
	if !pok {
		return false
	}
	switch r.Path[0] {
	case "branch", "tag", "sha":
		return true
	case "event":
		return mode == expr.ModeExact && (s == "merge_request_event" || s == "schedule" || s == "web")
	}
	return false
}

func TestWoodpeckerVectors(t *testing.T) {
	rawEnvs, cases := vectors.LoadWithEnvs(t, "expr")
	var envMaps []map[string]*string
	if err := json.Unmarshal(rawEnvs, &envMaps); err != nil || len(envMaps) < 4 {
		t.Fatalf("envs: %v (%d)", err, len(envMaps))
	}
	envs := make([]canonEnv, len(envMaps))
	for i, m := range envMaps {
		envs[i] = canonEnv{RefName: m["ref_name"], RefType: m["ref_type"], Event: m["event"], SHA: m["sha"], Nat: m["native"]}
	}
	count := map[string]int{}
	for i, c := range cases {
		var in struct {
			Dir  string          `json:"dir"`
			Expr json.RawMessage `json:"expr"`
		}
		var out vectorOut
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		if string(out.Tr) == "null" {
			continue
		}
		from := gha.Lang
		if in.Dir == dirRev {
			from = gitlab.Lang
		}
		src, err := decodeExpr(in.Expr, from.ID)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		glExpr, err := decodeExpr(out.Tr, gitlab.Lang.ID)
		if in.Dir == dirRev {
			glExpr = src
		} else if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		got, ok := expr.Translate(src, from, woodpecker.Lang)
		if want := inSubset(glExpr); ok != want || expr.Translatable(src, from, woodpecker.Lang) != ok {
			t.Errorf("case %d %s: Translate ok = %v (Translatable %v), subset says %v", i, expr.String(src), ok, expr.Translatable(src, from, woodpecker.Lang), want)
			continue
		}
		if !ok {
			count[in.Dir+" refused"]++
			continue
		}
		count[in.Dir+" translated"]++
		if text, err := woodpecker.Print(got); err != nil {
			t.Errorf("case %d: %s does not print: %v", i, expr.String(got), err)
		} else if again, err := reprint(text); err != nil || again != text {
			t.Errorf("case %d: %q prints again as %q (%v)", i, text, again, err)
		}
		for j, e := range envs {
			if g := woodpeckerHolds(t, got, e.woodpeckerView()); g != out.Src[j] && !unsetEventNarrower(e, g, out.Src[j]) {
				t.Errorf("case %d env %d %s -> %s: Woodpecker = %v, Lean source = %v", i, j, expr.String(src), expr.String(got), g, out.Src[j])
			}
		}
		if in.Dir == dirRev {
			checkOut(t, i, woodpecker.ReadIf(got), envs, out, count)
		}
	}
	for _, k := range []string{dirFwd + " translated", dirFwd + " refused", dirRev + " translated", dirRev + " refused", "woodpecker->gha"} {
		if count[k] < 20 {
			t.Errorf("only %d cases for %q; the vectors lost coverage", count[k], k)
		}
	}
	t.Logf("cases: %v", count)
}

// checkOut translates the Woodpecker form of a gitlab expression on to GHA and holds it to the Lean result.
func checkOut(t *testing.T, i int, wp expr.Expr, envs []canonEnv, out vectorOut, count map[string]int) {
	t.Helper()
	gh, ok := expr.Translate(wp, woodpecker.Lang, gha.Lang)
	if !ok && isEventList(wp) {
		count["event list, one way"]++
		return
	}
	if !ok {
		t.Errorf("case %d: %s translates to gitlab-class and gha but not from woodpecker to gha", i, expr.String(wp))
		return
	}
	count["woodpecker->gha"]++
	for j, e := range envs {
		if g := holdsOf(t, gh, e.ghaView(), gha.Lang.Sem); g != out.Tgt[j] {
			t.Errorf("case %d env %d %s -> %s: GHA = %v, Lean target = %v", i, j, expr.String(wp), expr.String(gh), g, out.Tgt[j])
		}
	}
}

func wpParse(t testing.TB, src string) expr.Expr {
	t.Helper()
	e, err := woodpecker.Parse(src)
	if err != nil {
		t.Fatalf("woodpecker.Parse(%q): %v", src, err)
	}
	return e
}

func parseIn(t testing.TB, d expr.Dialect, src string) expr.Expr {
	t.Helper()
	switch d.ID {
	case gha.Dialect:
		return ghaParse(t, src)
	case gitlab.Dialect:
		return glParse(t, src)
	}
	return wpParse(t, src)
}

func printIn(d expr.Dialect, e expr.Expr) (string, error) {
	switch d.ID {
	case gha.Dialect:
		return gha.Print(e)
	case gitlab.Dialect:
		return gitlab.Print(e)
	}
	return woodpecker.Print(e)
}

func TestWoodpeckerTranslateTable(t *testing.T) {
	for _, c := range []struct {
		from, to, src, want string // empty want: refused
	}{
		{"gha", "woodpecker", `github.event_name == 'pull_request'`, `CI_PIPELINE_EVENT matches "(?i)^pull_request$"`},
		{"gha", "woodpecker", `github.event_name == 'schedule'`, `CI_PIPELINE_EVENT matches "(?i)^cron$"`},
		{"gha", "woodpecker", `github.event_name != 'workflow_dispatch'`, `CI_PIPELINE_EVENT matches "(?i)^pull_request$" || CI_PIPELINE_EVENT matches "(?i)^push$" || CI_PIPELINE_EVENT matches "(?i)^tag$" || CI_PIPELINE_EVENT matches "(?i)^cron$"`},
		{"gha", "woodpecker", `github.ref_type == 'branch' && github.ref_name == 'main'`, `CI_COMMIT_BRANCH matches "(?i)^main$"`},
		{"gha", "woodpecker", `startsWith(github.sha, 'abc')`, `CI_COMMIT_SHA matches "(?i)^abc"`},
		// A tag run has CI_PIPELINE_EVENT "tag", GHA calls it push, so push has no exact counterpart.
		{"gha", "woodpecker", `github.event_name == 'push'`, ``},
		{"gha", "woodpecker", `github.ref_name == 'main'`, ``},
		{"gha", "woodpecker", `github.sha`, `CI_COMMIT_SHA != ""`},
		{"gha", "woodpecker", `!github.sha`, ``},
		{"gitlab", "woodpecker", `$CI_COMMIT_BRANCH =~ /^release-/i`, `CI_COMMIT_BRANCH matches "(?i)^release-"`},
		{"gitlab", "woodpecker", `$CI_PIPELINE_SOURCE =~ /^web$/i`, `CI_PIPELINE_EVENT matches "(?i)^manual$"`},
		{"gitlab", "woodpecker", `$CI_COMMIT_BRANCH =~ /^a/i && !($CI_COMMIT_SHA =~ /b$/i)`, `CI_COMMIT_BRANCH matches "(?i)^a" && !(CI_COMMIT_SHA matches "(?i)b$")`},
		// Outside the subset: an unset name is null in GitLab and "" in Woodpecker (Woodpecker.lean: not_gitlab_class).
		{"gitlab", "woodpecker", `$CI_COMMIT_TAG == ""`, ``},
		{"gitlab", "woodpecker", `$CI_COMMIT_TAG == null`, ``},
		// ref != "" holds for an unset ref in GitLab (null != "") and fails in Woodpecker, and it must not read back as the bare ref.
		{"gitlab", "woodpecker", `$CI_COMMIT_SHA != ""`, ``},
		{"gitlab", "woodpecker", `$CI_COMMIT_SHA != "" && $CI_COMMIT_BRANCH == "main"`, ``},
		{"gitlab", "woodpecker", `$CI_COMMIT_TAG`, `CI_COMMIT_TAG != ""`},
		{"gitlab", "woodpecker", `$CI_COMMIT_BRANCH == "main"`, `CI_COMMIT_BRANCH == "main"`},
		{"gitlab", "woodpecker", `$CI_COMMIT_BRANCH != "main" && $CI_COMMIT_TAG =~ /^v/i`, `CI_COMMIT_BRANCH != "main" && CI_COMMIT_TAG matches "(?i)^v"`},
		{"gitlab", "woodpecker", `$CI_COMMIT_BRANCH =~ /^main$/`, ``},
		{"gitlab", "woodpecker", `$CI_COMMIT_TAG && $CI_COMMIT_BRANCH`, ``},
		{"gitlab", "woodpecker", `$CI_PIPELINE_SOURCE == "web"`, `CI_PIPELINE_EVENT matches "(?i)^manual$"`},
		{"gitlab", "woodpecker", `$CI_PIPELINE_SOURCE != "schedule"`, `CI_PIPELINE_EVENT matches "(?i)^pull_request$" || CI_PIPELINE_EVENT matches "(?i)^push$" || CI_PIPELINE_EVENT matches "(?i)^tag$" || CI_PIPELINE_EVENT matches "(?i)^manual$"`},
		{"gitlab", "woodpecker", `$CI_PIPELINE_SOURCE == "push"`, ``},
		{"gitlab", "woodpecker", `$CI_PIPELINE_SOURCE == "WEB"`, ``},
		{"gitlab", "woodpecker", `$CI_PIPELINE_SOURCE`, ``},
		{"gitlab", "woodpecker", `$CI_COMMIT_BRANCH =~ /^v\d+/`, ``},
		{"gitlab", "woodpecker", `$CI_PIPELINE_SOURCE =~ /^push$/i`, ``},
		{"gitlab", "woodpecker", `$CI_COMMIT_REF_NAME =~ /^main$/i`, ``},
		{"woodpecker", "gitlab", `CI_COMMIT_BRANCH matches "(?i)^main$"`, `$CI_COMMIT_BRANCH =~ /^main$/i || $CI_MERGE_REQUEST_TARGET_BRANCH_NAME =~ /^main$/i`},
		{"woodpecker", "gitlab", `CI_PIPELINE_EVENT matches "(?i)^cron$"`, `$CI_PIPELINE_SOURCE =~ /^schedule$/i`},
		{"woodpecker", "gitlab", `CI_COMMIT_BRANCH matches "(?i)^a" || !(CI_COMMIT_SHA matches "(?i)b$")`, `$CI_COMMIT_BRANCH =~ /^a/i || $CI_MERGE_REQUEST_TARGET_BRANCH_NAME =~ /^a/i || !($CI_COMMIT_SHA =~ /b$/i)`},
		{"woodpecker", "gha", `CI_PIPELINE_EVENT matches "(?i)^pull_request$"`, `github.event_name == 'pull_request'`},
		{"woodpecker", "gha", `CI_COMMIT_SHA matches "(?i)^abc"`, `startsWith(github.sha, 'abc')`},
		{"woodpecker", "gitlab", `CI_COMMIT_TAG == ""`, ``},
		{"woodpecker", "gitlab", `CI_COMMIT_TAG != nil`, ``},
		// A bare ref is how the IR holds ref != "" (woodpecker.ReadIf); the provider keeps a bare name that
		// expr-lang rejects as text, so the dialect only sees the bare form where it means non-empty.
		{"woodpecker", "gitlab", `CI_COMMIT_SHA`, `$CI_COMMIT_SHA`},
		{"woodpecker", "gha", `CI_COMMIT_SHA`, `github.sha`},
		{"woodpecker", "gitlab", `CI_PIPELINE_EVENT`, ``},
		{"woodpecker", "gitlab", `CI_COMMIT_SHA && CI_COMMIT_TAG`, ``},
		{"woodpecker", "gha", `!CI_COMMIT_SHA`, ``},
		{"woodpecker", "gitlab", `CI_COMMIT_SHA != ""`, ``},
		{"woodpecker", "gitlab", `CI_COMMIT_SHA contains "ab"`, ``},
		{"woodpecker", "gitlab", `CI_COMMIT_BRANCH matches "^v\\d+"`, ``},
		{"woodpecker", "gitlab", `CI_PIPELINE_EVENT matches "(?i)^tag$"`, `$CI_COMMIT_TAG =~ /^.+$/i`},
		{"woodpecker", "gitlab", `SKIP matches "(?i)^x$"`, ``},
		{"woodpecker", "gitlab", `CI_COMMIT_BRANCH matches "(?i)^a" && CI_REPO_URL matches "(?i)^b"`, ``},
	} {
		from, ok := expr.LangOf(c.from)
		to, ok2 := expr.LangOf(c.to)
		if !ok || !ok2 {
			t.Fatalf("dialects %s, %s not registered", c.from, c.to)
		}
		src := parseIn(t, *from, c.src)
		got, ok := expr.Translate(src, *from, *to)
		if tb := expr.Translatable(src, *from, *to); tb != ok {
			t.Errorf("%s -> %s %q: Translatable = %v, Translate = %v", c.from, c.to, c.src, tb, ok)
		}
		if c.want == "" {
			if ok {
				t.Errorf("%s -> %s %q translated to %s, want refusal", c.from, c.to, c.src, expr.String(got))
			}
			if _, _, approx := expr.TranslateApprox(src, *from, *to); approx && c.from == woodpecker.Dialect {
				t.Errorf("%s -> %s %q: TranslateApprox accepted a non-subset source", c.from, c.to, c.src)
			}
			continue
		}
		if !ok {
			t.Errorf("%s -> %s %q refused", c.from, c.to, c.src)
			continue
		}
		if text, err := printIn(*to, got); err != nil || text != c.want {
			t.Errorf("%s -> %s %q = %q, %v; want %q", c.from, c.to, c.src, text, err, c.want)
		}
	}
}

// Random gitlab-subset expressions on random environments: a translation to Woodpecker keeps the truthiness,
// with an unset ref null in GitLab and "" in Woodpecker.
func TestWoodpeckerRandomAgreement(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e := genNode(t, 3, true)
		out, ok := expr.Translate(e, gitlab.Lang, woodpecker.Lang)
		if expr.Translatable(e, gitlab.Lang, woodpecker.Lang) != ok {
			t.Fatalf("Translatable != Translate for %s", expr.String(e))
		}
		if !ok {
			return
		}
		env := genCanonEnv(t)
		want := holdsOf(t, e, env.gitlabView(), gitlab.Lang.Sem)
		if got := woodpeckerHolds(t, out, env.woodpeckerView()); got != want && !unsetEventNarrower(env, got, want) {
			t.Fatalf("%s -> %s: Woodpecker %v, GitLab %v, env %+v", expr.String(e), expr.String(out), got, want, env)
		}
		// GitLab cannot print a one-character substring pattern, so such a source has no way back.
		if !gitlab.Lang.Supports(e) {
			return
		}
		// An event == or != comes back as the pattern test it was spelled as (Woodpecker reads no event equality).
		if isEventList(out) || strings.Contains(expr.String(e), "event ==") || strings.Contains(expr.String(e), "event !=") || strings.Contains(expr.String(e), "== event") || strings.Contains(expr.String(e), "!= event") {
			return
		}
		back, ok := expr.Translate(woodpecker.ReadIf(out), woodpecker.Lang, gitlab.Lang)
		// The way back also states the pull request target branch, which no GitLab view in this test sets.
		if !ok || !reflect.DeepEqual(back, e) && holdsOf(t, back, env.gitlabView(), gitlab.Lang.Sem) != want {
			t.Fatalf("%s did not return from Woodpecker: %v, %v", expr.String(e), back, ok)
		}
	})
}

// unsetEventNarrower: a Woodpecker run always has an event, so a source run with none has no Woodpecker twin. A
// positive event list is false there while the source complement is true, which only runs less.
func unsetEventNarrower(e canonEnv, got, src bool) bool { return e.Event == nil && !got && src }

// isEventList: the translation spelled a complement of events as a list that names push and tag, which Woodpecker
// has no way to hand back (its table has no push entry).
func isEventList(e expr.Expr) bool {
	s := expr.String(e)
	return strings.Contains(s, "^push$") || strings.Contains(s, "^tag$")
}
