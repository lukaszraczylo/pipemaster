package drone_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/drone"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

// run is one pipeline run as the three dialects report it.
type run struct{ name, typ, event string }

var runs = []run{
	{"main", "branch", "push"}, {"dev", "branch", "push"}, {"v1", "tag", "push"},
	{"feature", "branch", "pull_request"}, {"main", "branch", "pull_request"}, {"merge", "branch", "pull_request"},
}

type vals map[string]string

func (v vals) Lookup(p []string) (expr.Value, bool) {
	s, ok := v[strings.Join(p, ".")]
	if !ok {
		return nil, false
	}
	return s, true
}
func (vals) Status() expr.Status { return expr.Success }

// target is the branch a Drone pull request run reports in DRONE_BRANCH.
const target = "main"

func (r run) view(d expr.Dialect) vals {
	pr := r.event == "pull_request"
	v := vals{"ref_type": r.typ, "sha": "abc"}
	switch d.ID {
	case exprgha.Dialect:
		v["ref_name"], v["event"] = r.name, r.event
		if pr {
			v["ref_name"] = "7/merge"
		}
	case exprgitlab.Dialect:
		v["ref_name"], v["event"] = r.name, exprgitlab.Events[r.event]
		if !pr {
			v[r.typ] = r.name
		}
	default:
		v["event"] = r.event
		switch {
		case pr:
			v["branch"] = target
		case r.typ == "branch":
			v["branch"] = r.name
		default:
			v["tag"] = r.name
		}
	}
	return v
}

func (r run) holds(t *testing.T, e expr.Expr, d expr.Dialect) bool {
	t.Helper()
	v, err := expr.Eval(e, r.view(d), d.Sem)
	if err != nil {
		t.Fatalf("Eval(%s): %v", expr.String(e), err)
	}
	if d.Sem.Truthy != nil {
		return d.Sem.Truthy(v)
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	}
	return true
}

// A Drone translation must not run where its source does not. On a pull request Drone reports the target
// branch, GitLab leaves the branch unset and GHA names the ref <number>/merge.
func TestDroneTranslationNeverRunsMoreOnPullRequests(t *testing.T) {
	sources := map[string][]string{
		exprgha.Dialect: {
			`github.ref_name == 'main'`, `github.ref_type == 'branch' && github.ref_name == 'main'`,
			`!(github.ref_type == 'branch' && github.ref_name == 'main')`, `github.ref_type == 'tag' && github.ref_name == 'v1'`,
			`github.event_name == 'push' && github.ref_type == 'branch' && github.ref_name == 'main'`,
		},
		exprgitlab.Dialect: {
			`$CI_COMMIT_BRANCH == "main"`, `$CI_COMMIT_BRANCH != "main"`, `$CI_COMMIT_BRANCH =~ /^main$/`,
			`$CI_COMMIT_BRANCH !~ /^main$/`, `$CI_COMMIT_TAG == "v1"`, `$CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH == "main"`,
			`$CI_COMMIT_REF_NAME == "main"`, `$CI_COMMIT_REF_NAME != "main"`,
		},
	}
	translated := 0
	for from, srcs := range sources {
		fd, _ := expr.LangOf(from)
		for _, text := range srcs {
			var src expr.Expr
			var err error
			if from == exprgha.Dialect {
				src, err = exprgha.Parse(text)
			} else {
				src, err = exprgitlab.Parse(text)
			}
			if err != nil {
				t.Fatalf("parse %q: %v", text, err)
			}
			got, ok := expr.TranslateFacts(src, *fd, drone.Lang, expr.Facts{})
			if !ok {
				continue
			}
			translated++
			for _, r := range runs {
				if r.holds(t, got, drone.Lang) && !r.holds(t, src, *fd) {
					t.Errorf("%s -> drone: %q runs on %+v, the source does not: %s", from, text, r, expr.String(got))
				}
			}
		}
	}
	if translated == 0 {
		t.Fatal("no source translated")
	}
}
