package drone_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/drone"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	exprwp "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
)

func TestDroneConditionTranslates(t *testing.T) {
	prPlain := bin(expr.OpAnd, eq(event, "pull_request"), eq(branch, "main"))
	pushBranch := bin(expr.OpAnd, eq(event, "push"), eq(r("ref_type"), "branch"))
	for _, c := range []struct {
		name   string
		want   string
		cond   expr.Expr
		to     expr.Dialect
		approx bool
	}{
		{"pull request on main to GitLab", `$CI_PIPELINE_SOURCE == "merge_request_event" && $CI_COMMIT_BRANCH == "main"`, prPlain, exprgitlab.Lang, false},
		{"main to Woodpecker", `CI_COMMIT_BRANCH == "main"`, eq(branch, "main"), exprwp.Lang, false},
		{"pull request on main to GHA", "", prPlain, exprgha.Lang, true},
		{"push of a branch to GHA", "", pushBranch, exprgha.Lang, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := expr.Translate(c.cond, drone.Lang, c.to)
			if c.approx {
				var cav []string
				if got, cav, ok = expr.TranslateApprox(c.cond, drone.Lang, c.to); !ok || len(cav) == 0 {
					t.Fatalf("TranslateApprox = %v, caveats %v, %v; want an approximate translation", got, cav, ok)
				}
				return
			}
			if !ok {
				t.Fatalf("Translate(%s) failed", expr.String(c.cond))
			}
			var text string
			var err error
			switch c.to.ID {
			case exprgitlab.Dialect:
				text, err = exprgitlab.Print(got)
			default:
				text, err = exprwp.Print(got)
			}
			if err != nil || text != c.want {
				t.Errorf("printed %q, %v; want %q", text, err, c.want)
			}
		})
	}
}

func TestDroneGlobAndStatusDoNotTranslate(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"a glob":      drone.GlobOpaque("branch", "release/*"),
		"a status":    expr.Call{Name: "failure"},
		"a bare ref":  branch,
		"inequality":  bin(expr.OpNe, branch, str("main")),
		"empty value": eq(branch, ""),
	} {
		if got, ok := expr.Translate(e, drone.Lang, exprgitlab.Lang); ok {
			t.Errorf("%s translated to %v", name, got)
		}
	}
}
