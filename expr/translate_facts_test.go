package expr_test

// Trigger facts: a pipeline whose triggers rule out tag runs lets the translator state GHA ref_name
// comparisons through the branch ref (lean4/Pipemaster/TranslateFacts.lean: trFacts_sound).

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/internal/vectors"
)

const (
	dirFacts = "gha-gitlab-facts"
	dirGuard = "gha-gitlab-guard"
)

var branchFacts = expr.Facts{Branch: true}

func loadFactEnvs(t *testing.T) ([]canonEnv, []vectors.Case) {
	t.Helper()
	return loadEnvs(t, "expr-facts")
}

func loadEnvs(t *testing.T, name string) ([]canonEnv, []vectors.Case) {
	t.Helper()
	rawEnvs, cases := vectors.LoadWithEnvs(t, name)
	var maps []map[string]*string
	if err := json.Unmarshal(rawEnvs, &maps); err != nil || len(maps) < 4 {
		t.Fatalf("envs: %v (%d)", err, len(maps))
	}
	envs := make([]canonEnv, len(maps))
	for i, m := range maps {
		envs[i] = canonEnv{RefName: m["ref_name"], RefType: m["ref_type"], Event: m["event"], SHA: m["sha"], Nat: m["native"]}
	}
	return envs, cases
}

// factOut is one vector result; a null truthiness is an environment the case's facts rule out.
type factOut struct {
	Tr   json.RawMessage `json:"tr"`
	Src  []*bool         `json:"src"`
	Tgt  []*bool         `json:"tgt"`
	Wp   []*bool         `json:"wp"`
	WpOK bool            `json:"wp_ok"`
}

// factWoodpeckerView is what a Woodpecker run presents (Mirrors wpMap in Pipemaster/TranslateFacts.lean): on a
// pull request CI_COMMIT_BRANCH is the target branch and there is no CI_COMMIT_TAG.
func (e canonEnv) factWoodpeckerView() expr.Env {
	v := e.gitlabView()
	delete(v, "event")
	if e.Event != nil {
		v["event"] = wpEvents[*e.Event]
	}
	if e.Event != nil && *e.Event == "pull_request" {
		delete(v, "tag")
		v["branch"] = wpTarget
	}
	return woodpecker.UnsetEnv{Env: v}
}

// wpTarget is the target branch of a Woodpecker pull request run in the vectors (wpTarget in lean4/GenVectors.lean).
const wpTarget = "main"

func eqAllowed(t *testing.T, i, j int, what string, got bool, want *bool) {
	t.Helper()
	if want != nil && got != *want {
		t.Errorf("case %d env %d: %s = %v, Lean = %v", i, j, what, got, *want)
	}
}

// TestTranslateFactsVectors checks Go against the Lean model: the rewrite and translation agree with the
// vectors, the GitLab-class target keeps the source's truthiness, and so does Woodpecker on its own environment.
func TestTranslateFactsVectors(t *testing.T) {
	envs, cases := loadFactEnvs(t)
	count := map[string]int{}
	for i, c := range cases {
		var in struct {
			Dir  string          `json:"dir"`
			Expr json.RawMessage `json:"expr"`
		}
		var out factOut
		if err := json.Unmarshal(c.In, &in); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(c.Out, &out); err != nil {
			t.Fatal(err)
		}
		facts := map[string]expr.Facts{dirFacts: branchFacts, dirGuard: {}}[in.Dir]
		if in.Dir != dirFacts && in.Dir != dirGuard {
			t.Fatalf("case %d: direction %q", i, in.Dir)
		}
		src, err := decodeExpr(in.Expr, gha.Lang.ID)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		got, ok := expr.Translate(expr.Assume(src, facts.Branch), gha.Lang, gitlab.Lang)
		if string(out.Tr) == "null" {
			count["refused"]++
			if ok {
				t.Errorf("case %d %s: Lean refuses, Go translated to %s", i, expr.String(src), expr.String(got))
			}
			continue
		}
		want, err := decodeExpr(out.Tr, gitlab.Lang.ID)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("case %d %s: Go = %v, %v; Lean = %s", i, expr.String(src), got, ok, expr.String(want))
			continue
		}
		count[in.Dir]++
		if len(out.Src) != len(envs) || len(out.Tgt) != len(envs) || len(out.Wp) != len(envs) {
			t.Fatalf("case %d: result lengths for %d envs", i, len(envs))
		}
		for j, e := range envs {
			s := holdsOf(t, src, e.ghaView(), gha.Lang.Sem)
			eqAllowed(t, i, j, "GHA source", s, out.Src[j])
			if out.Src[j] != nil {
				eqAllowed(t, i, j, "GitLab-class target", holdsOf(t, got, e.gitlabView(), gitlab.Lang.Sem), out.Tgt[j])
				if g := holdsOf(t, got, e.gitlabView(), gitlab.Lang.Sem); g != s {
					t.Errorf("case %d env %d %s -> %s: target %v, source %v", i, j, expr.String(src), expr.String(got), g, s)
				}
			}
		}
		checkFactsWoodpecker(t, i, src, facts, envs, out, count)
	}
	for _, k := range []string{dirFacts, dirGuard, "refused", "woodpecker"} {
		if count[k] < 20 {
			t.Errorf("only %d cases for %q; the vectors lost coverage", count[k], k)
		}
	}
	t.Logf("cases: %v", count)
}

// checkFactsWoodpecker runs the facts translation to Woodpecker and holds it to the Lean source result on
// Woodpecker's environment, where a pull request run reports the target branch.
func checkFactsWoodpecker(t *testing.T, i int, src expr.Expr, facts expr.Facts, envs []canonEnv, out factOut, count map[string]int) {
	t.Helper()
	wp, ok := expr.TranslateFacts(src, gha.Lang, woodpecker.Lang, facts)
	if tb := expr.TranslatableFacts(src, gha.Lang, woodpecker.Lang, facts); tb != ok {
		t.Errorf("case %d %s: TranslatableFacts = %v, TranslateFacts = %v", i, expr.String(src), tb, ok)
	}
	if !ok {
		return
	}
	if !out.WpOK {
		t.Errorf("case %d %s: Go translates to Woodpecker, Lean model refuses (guardOk)", i, expr.String(src))
	}
	count["woodpecker"]++
	if text, err := woodpecker.Print(wp); err != nil {
		t.Errorf("case %d: %s does not print: %v", i, expr.String(wp), err)
	} else if again, err := reprint(text); err != nil || again != text {
		t.Errorf("case %d: %q prints again as %q (%v)", i, text, again, err)
	}
	for j, e := range envs {
		if out.Src[j] == nil {
			continue
		}
		g := woodpeckerHolds(t, wp, e.factWoodpeckerView())
		if g != *out.Src[j] || g != *out.Wp[j] {
			t.Errorf("case %d env %d %s -> %s: Woodpecker = %v, GHA source = %v, Lean wp = %v", i, j, expr.String(src), expr.String(wp), g, *out.Src[j], *out.Wp[j])
		}
	}
}

func TestTranslateFactsTable(t *testing.T) {
	for _, c := range []struct {
		src    string
		target string
		want   string // empty: refused
		facts  expr.Facts
	}{
		{`github.ref_name == 'main'`, "woodpecker",
			`!(CI_PIPELINE_EVENT matches "(?i)^pull_request$") && CI_COMMIT_BRANCH matches "(?i)^main$"`, branchFacts},
		{`github.ref_name != 'main'`, "woodpecker",
			`CI_PIPELINE_EVENT matches "(?i)^pull_request$" || !(CI_COMMIT_BRANCH matches "(?i)^main$")`, branchFacts},
		{`'main' == github.ref_name`, "woodpecker",
			`!(CI_PIPELINE_EVENT matches "(?i)^pull_request$") && CI_COMMIT_BRANCH matches "(?i)^main$"`, branchFacts},
		{`github.ref_name == 'main' || github.event_name == 'schedule'`, "woodpecker",
			`!(CI_PIPELINE_EVENT matches "(?i)^pull_request$") && CI_COMMIT_BRANCH matches "(?i)^main$" || CI_PIPELINE_EVENT matches "(?i)^cron$"`, branchFacts},
		// No facts: refused as before.
		{`github.ref_name == 'main'`, "woodpecker", ``, expr.Facts{}},
		// A branch guard gains the pull request test: CI_COMMIT_BRANCH is the target branch there.
		{`github.ref_type == 'branch' && github.ref_name == 'main'`, "woodpecker",
			`!(CI_PIPELINE_EVENT matches "(?i)^pull_request$") && CI_COMMIT_BRANCH matches "(?i)^main$"`, branchFacts},
		// Without the facts the guarded form still excludes pull requests: Woodpecker reports the target branch there.
		{`github.ref_type == 'branch' && github.ref_name == 'main'`, "woodpecker",
			`!(CI_PIPELINE_EVENT matches "(?i)^pull_request$") && CI_COMMIT_BRANCH matches "(?i)^main$"`, expr.Facts{}},
		{`github.ref_type == 'branch' && github.ref_name != 'main'`, "woodpecker", ``, expr.Facts{}},
		{`github.ref_type == 'branch' && startsWith(github.ref_name, 'rel')`, "woodpecker", ``, expr.Facts{}},
		// A tag guard names no branch and keeps its shape.
		{`github.ref_type == 'tag' && github.ref_name == 'v1'`, "woodpecker",
			`CI_COMMIT_TAG matches "(?i)^v1$"`, branchFacts},
		// A branch-guarded affix test is unsafe on a pull request, so it is refused under the facts.
		{`github.ref_type == 'branch' && startsWith(github.ref_name, 'rel')`, "woodpecker", ``, branchFacts},
		// A prefix test cannot be stated: a pull request ref_name N/merge starts with a digit.
		{`startsWith(github.ref_name, 'rel')`, "woodpecker", ``, branchFacts},
		// A literal that starts with a digit could equal N/merge on a pull request.
		{`github.ref_name == '12/merge'`, "woodpecker", ``, branchFacts},
		{`github.ref_name == ''`, "woodpecker", ``, branchFacts},
	} {
		to, ok := expr.LangOf(c.target)
		if !ok {
			t.Fatalf("dialect %s not registered", c.target)
		}
		src := ghaParse(t, c.src)
		got, ok := expr.TranslateFacts(src, gha.Lang, *to, c.facts)
		if tb := expr.TranslatableFacts(src, gha.Lang, *to, c.facts); tb != ok {
			t.Errorf("%q: TranslatableFacts = %v, TranslateFacts = %v", c.src, tb, ok)
		}
		if c.want == "" {
			if ok {
				t.Errorf("%q translated to %s, want refusal", c.src, expr.String(got))
			}
			continue
		}
		if !ok {
			t.Errorf("%q refused", c.src)
			continue
		}
		if text, err := printIn(*to, got); err != nil || text != c.want {
			t.Errorf("%q = %q, %v; want %q", c.src, text, err, c.want)
		}
	}
}

// A target that names ref_name takes the plain path, facts or not.
func TestTranslateFactsKeepsPlain(t *testing.T) {
	src := ghaParse(t, `github.ref_name == 'main'`)
	plain, ok := expr.Translate(src, gha.Lang, gitlab.Lang)
	got, ok2 := expr.TranslateFacts(src, gha.Lang, gitlab.Lang, branchFacts)
	if !ok || !ok2 || !reflect.DeepEqual(plain, got) {
		t.Errorf("plain %v, %v; with facts %v, %v", plain, ok, got, ok2)
	}
}
