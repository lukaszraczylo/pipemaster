package expr_test

// The fail-safe rule on a pull request: a translation must never hold on a run where its source does not. Each
// dialect reports the branch differently there: GHA ref_name is <number>/merge, GitLab leaves CI_COMMIT_BRANCH
// unset in a merge request pipeline, Woodpecker holds the target branch.

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
)

type prRun struct {
	name, typ, event string
	// sfx follows the Woodpecker pull request event name: pull_request_closed, pull_request_metadata.
	sfx string
}

var prRuns = []prRun{
	{"main", "branch", "push", ""},
	{"dev", "branch", "push", ""},
	{"hotfix-merge", "branch", "push", ""},
	{"release/1", "branch", "push", ""},
	{"feature", "branch", "pull_request", ""},
	{"main", "branch", "pull_request", ""},
	{"merge", "branch", "pull_request", ""},
	{"v1", "tag", "push", ""},
	{"feature", "branch", "pull_request", "_closed"},
	{"main", "branch", "pull_request", "_closed"},
	{"merge", "branch", "pull_request", "_metadata"},
}

// holds evaluates e as dialect d reports run r. A pull request has the ref name 7/merge in GHA, no branch and no
// tag in GitLab, and the target branch in Woodpecker.
func (r prRun) holds(t *testing.T, e expr.Expr, d expr.Dialect) bool {
	t.Helper()
	pr := r.event == "pull_request"
	c := canonEnv{RefName: sp(r.name), RefType: sp(r.typ), Event: sp(r.event), SHA: sp("abc")}
	switch d.ID {
	case gha.Dialect:
		if pr {
			c.RefName = sp("7/merge")
		}
		return holdsOf(t, e, withBase(c.ghaView(), pr), gha.Lang.Sem)
	case gitlab.Dialect:
		// CI_COMMIT_REF_NAME holds the source branch in a merge request pipeline (see AssumeRefName), which c.RefName is.
		return holdsOf(t, e, withBase(c.glView(), pr), gitlab.Lang.Sem)
	}
	return woodpeckerHolds(t, e, c.factWoodpeckerView(r.sfx))
}

// withBase sets the target branch of a pull request run, which the Woodpecker run of the same name holds in its branch.
func withBase(v view, pr bool) view {
	if pr {
		v["pr.base_ref"] = wpTarget
	}
	return v
}

var prSources = map[string][]string{
	gha.Dialect: {
		`github.ref_name == 'main'`,
		`github.ref_name != 'main'`,
		`github.ref_type == 'branch' && github.ref_name == 'main'`,
		`!(github.ref_type == 'branch' && github.ref_name == 'main')`,
		`github.ref_type == 'branch' && startsWith(github.ref_name, 'rel')`,
		`!(github.ref_type == 'branch' && startsWith(github.ref_name, 'rel'))`,
		`!(github.ref_type == 'branch' && startsWith(github.ref_name, '7'))`,
		`github.ref_type == 'branch' && endsWith(github.ref_name, 'merge')`,
		`!(github.ref_type == 'branch' && endsWith(github.ref_name, 'merge'))`,
		`!(github.ref_type == 'branch' && endsWith(github.ref_name, 'fix'))`,
		`!(github.ref_type == 'branch' && contains(github.ref_name, 'merge'))`,
		`!(github.ref_type == 'branch' && contains(github.ref_name, 'ma'))`,
		`github.ref_type == 'tag' && github.ref_name == 'v1'`,
		`!(github.ref_type == 'tag' && github.ref_name == 'v1')`,
		`github.event_name == 'pull_request' || github.ref_name == 'main'`,
	},
	gitlab.Dialect: {
		`$CI_COMMIT_BRANCH == "main"`,
		`$CI_COMMIT_BRANCH != "main"`,
		`$CI_COMMIT_BRANCH =~ /^main$/i`,
		`$CI_COMMIT_BRANCH !~ /^main$/i`,
		`$CI_COMMIT_BRANCH =~ /^rel/i`,
		`$CI_COMMIT_BRANCH =~ /^7/i`,
		`$CI_COMMIT_BRANCH =~ /merge/i`,
		`$CI_COMMIT_BRANCH =~ /merge$/i`,
		`$CI_COMMIT_BRANCH =~ /xyz/i`,
		`$CI_COMMIT_BRANCH !~ /merge/i`,
		`!($CI_COMMIT_BRANCH =~ /merge/i)`,
		`!($CI_COMMIT_BRANCH !~ /merge/i)`,
		`!($CI_COMMIT_BRANCH =~ /^main$/i)`,
		`$CI_COMMIT_BRANCH =~ /^main$/i || $CI_PIPELINE_SOURCE == "merge_request_event"`,
		`$CI_COMMIT_TAG =~ /^v1$/i`,
	},
	woodpecker.Dialect: {
		`CI_COMMIT_BRANCH == "main"`,
		`CI_COMMIT_BRANCH != "main"`,
		`CI_COMMIT_BRANCH matches "(?i)^main$"`,
		`!(CI_COMMIT_BRANCH matches "(?i)^main$")`,
		`CI_COMMIT_BRANCH matches "(?i)^rel"`,
		`CI_COMMIT_BRANCH matches "(?i)merge"`,
		`!(CI_COMMIT_BRANCH matches "(?i)merge")`,
		`CI_COMMIT_BRANCH matches "(?i)xyz"`,
		`!(CI_COMMIT_BRANCH matches "(?i)xyz")`,
		`CI_COMMIT_BRANCH matches "(?i)^main$" || CI_PIPELINE_EVENT matches "(?i)^pull_request$"`,
	},
}

func TestTranslationNeverRunsMoreOnPullRequests(t *testing.T) {
	langs := map[string]expr.Dialect{gha.Dialect: gha.Lang, gitlab.Dialect: gitlab.Lang, woodpecker.Dialect: woodpecker.Lang}
	count := map[string]int{}
	for from, srcs := range prSources {
		for _, text := range srcs {
			src := parseIn(t, langs[from], text)
			for to, td := range langs {
				if to == from {
					continue
				}
				got, ok := expr.TranslateFacts(src, langs[from], td, expr.Facts{})
				if !ok {
					if got, _, ok = expr.TranslateApprox(src, langs[from], td); !ok {
						count[from+"->"+to+" refused"]++
						continue
					}
				}
				count[from+"->"+to+" translated"]++
				for _, r := range prRuns {
					if r.holds(t, got, td) && !r.holds(t, src, langs[from]) {
						t.Errorf("%s -> %s: %q runs on %+v, the source does not: %s", from, to, text, r, expr.String(got))
					}
				}
			}
		}
	}
	t.Logf("%v", count)
}

// The rules refuse only what can run more: these shapes stay translatable.
func TestPullRequestBranchKeepsSafeShapes(t *testing.T) {
	for _, c := range []struct{ from, to, src string }{
		{gitlab.Dialect, gha.Dialect, `$CI_COMMIT_BRANCH =~ /^rel/i`},
		{gitlab.Dialect, gha.Dialect, `$CI_COMMIT_BRANCH =~ /^main$/i`},
		{gitlab.Dialect, gha.Dialect, `$CI_COMMIT_BRANCH !~ /merge/i`},
		{gitlab.Dialect, gha.Dialect, `$CI_COMMIT_BRANCH == "main"`},
		{gitlab.Dialect, woodpecker.Dialect, `$CI_COMMIT_BRANCH !~ /^main$/i`},
		{gha.Dialect, gitlab.Dialect, `github.ref_type == 'branch' && startsWith(github.ref_name, 'rel')`},
		{gha.Dialect, gitlab.Dialect, `!(github.ref_type == 'branch' && github.ref_name == 'main')`},
		{woodpecker.Dialect, gha.Dialect, `CI_COMMIT_BRANCH matches "(?i)^main$"`},
		{woodpecker.Dialect, gitlab.Dialect, `CI_COMMIT_BRANCH matches "(?i)^main$"`},
	} {
		from, _ := expr.LangOf(c.from)
		to, _ := expr.LangOf(c.to)
		src := parseIn(t, *from, c.src)
		_, ok := expr.TranslateFacts(src, *from, *to, expr.Facts{})
		if _, _, aok := expr.TranslateApprox(src, *from, *to); !ok && !aok {
			t.Errorf("%s -> %s: %q refused", c.from, c.to, c.src)
		}
	}
}
