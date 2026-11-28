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
		{"pull request on main to GitLab", `$CI_PIPELINE_SOURCE == "merge_request_event" && ($CI_COMMIT_BRANCH == "main" || $CI_MERGE_REQUEST_TARGET_BRANCH_NAME == "main")`, prPlain, exprgitlab.Lang, false},
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

// A Drone tag push is the tag event and a pull request holds the target branch in the branch: both translate to
// a GitLab and a GHA that have neither (expr/variants.go).
func TestDroneTagAndBranchVariants(t *testing.T) {
	tag := eq(expr.Ref{Path: []string{expr.CtxEvent}}, "tag")
	for name, c := range map[string]expr.Expr{"tag": tag, "tag or main": expr.Binary{Op: expr.OpOr, L: tag, R: eq(branch, "main")}} {
		for _, to := range []expr.Dialect{exprgitlab.Lang, exprgha.Lang} {
			if _, _, ok := expr.TranslateApprox(c, drone.Lang, to); !ok {
				t.Errorf("%s -> %s: no translation", name, to.ID)
			}
		}
	}
}

// droneRun is one run as Drone and each target dialect report it, keyed by canonical name.
type droneRun struct {
	src, gha, gitlab vals
	name             string
}

var droneRuns = []droneRun{
	{name: "push main", src: vals{"event": "push", "branch": "main"}, gha: vals{"event": "push", "ref_type": "branch", "ref_name": "main"}, gitlab: vals{"event": "push", "branch": "main"}},
	{name: "push feature", src: vals{"event": "push", "branch": "feature"}, gha: vals{"event": "push", "ref_type": "branch", "ref_name": "feature"}, gitlab: vals{"event": "push", "branch": "feature"}},
	{name: "tag", src: vals{"event": "tag", "tag": "v1"}, gha: vals{"event": "push", "ref_type": "tag", "ref_name": "v1", "tag": "v1"}, gitlab: vals{"event": "push", "tag": "v1"}},
	{name: "pull request to main", src: vals{"event": "pull_request", "branch": "main"}, gha: vals{"event": "pull_request", "ref_type": "branch", "ref_name": "7/merge"}, gitlab: vals{"event": "merge_request_event", "pr.base_ref": "main"}},
}

// A translation from Drone must not run on a run where the Drone condition does not, and it must run on at least
// one run where it does.
func TestDroneVariantsNeverRunMoreThanTheSource(t *testing.T) {
	tag := eq(expr.Ref{Path: []string{expr.CtxEvent}}, "tag")
	main := eq(branch, "main")
	conds := map[string]expr.Expr{
		"tag": tag, "main": main, "tag or main": bin(expr.OpOr, tag, main), "not tag": expr.Unary{Op: expr.OpNot, X: tag},
	}
	holds := func(e expr.Expr, v vals, d expr.Dialect) bool {
		t.Helper()
		got, err := expr.Eval(e, v, d.Sem)
		if err != nil {
			t.Fatalf("Eval(%s): %v", expr.String(e), err)
		}
		switch x := got.(type) {
		case nil:
			return false
		case bool:
			return x
		case string:
			return x != ""
		}
		return true
	}
	for name, c := range conds {
		for _, to := range []expr.Dialect{exprgha.Lang, exprgitlab.Lang} {
			out, _, ok := expr.TranslateApprox(c, drone.Lang, to)
			if x, exact := expr.Translate(c, drone.Lang, to); exact {
				out, ok = x, true
			}
			if !ok {
				t.Errorf("%s -> %s: no translation", name, to.ID)
				continue
			}
			ran := false
			for _, r := range droneRuns {
				tv := r.gha
				if to.ID == exprgitlab.Dialect {
					tv = r.gitlab
				}
				s, g := holds(c, r.src, drone.Lang), holds(out, tv, to)
				ran = ran || g
				if g && !s {
					t.Errorf("%s -> %s on %s: the target runs, Drone does not: %s", name, to.ID, r.name, expr.String(out))
				}
			}
			if !ran {
				t.Errorf("%s -> %s: the target never holds, so the test proves nothing: %s", name, to.ID, expr.String(out))
			}
		}
	}
}
