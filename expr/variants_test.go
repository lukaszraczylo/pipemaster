package expr_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
)

type varEnv map[string]expr.Value

func (m varEnv) Lookup(p []string) (expr.Value, bool) { v, ok := m[join(p)]; return v, ok }
func (m varEnv) Status() expr.Status {
	var s expr.Status
	return s
}

// run is one pipeline run seen by the source and by each target dialect.
type run struct {
	src      varEnv
	gha      varEnv
	gl       varEnv
	name     string
	prTarget string
	tagRun   bool
	prRun    bool
}

func fullRun(name, srcEvent, srcBranch, srcTag, base string) run {
	r := run{name: name, src: varEnv{"event": srcEvent, "branch": srcBranch, "tag": srcTag, "pr.base_ref": base}}
	switch srcEvent {
	case "tag":
		r.gha = varEnv{"event": "push", "ref_type": "tag", "ref_name": srcTag, "tag": srcTag, "branch": "", "pr.base_ref": "", "ref": "refs/tags/" + srcTag}
		r.gl = varEnv{"event": "push", "tag": srcTag, "branch": nil, "pr.base_ref": nil}
	case "manual":
		// A manual run on a tag ref sets the tag name, and its event is not the tag event.
		r.src = varEnv{"event": "manual", "branch": nil, "tag": srcTag, "pr.base_ref": nil}
		r.gha = varEnv{"event": "workflow_dispatch", "ref_type": "tag", "ref_name": srcTag, "tag": srcTag, "branch": "", "pr.base_ref": "", "ref": "refs/tags/" + srcTag}
		r.gl = varEnv{"event": "web", "tag": srcTag, "branch": nil, "pr.base_ref": nil}
	case "pull_request":
		r.gha = varEnv{"event": "pull_request", "ref_type": "branch", "ref_name": "5/merge", "pr.base_ref": base, "ref": "refs/pull/5/merge"}
		r.gl = varEnv{"event": "merge_request_event", "tag": nil, "branch": nil, "pr.base_ref": base}
	default:
		r.gha = varEnv{"event": "push", "ref_type": "branch", "ref_name": srcBranch, "pr.base_ref": "", "ref": "refs/heads/" + srcBranch}
		r.gl = varEnv{"event": "push", "tag": nil, "branch": srcBranch, "pr.base_ref": nil}
	}
	return r
}

func ev(t *testing.T, e expr.Expr, env varEnv, sem expr.Semantics) bool {
	t.Helper()
	v, err := expr.Eval(e, env, sem)
	if err != nil {
		t.Fatalf("eval %v: %v", e, err)
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	}
	return v != nil
}

// translateBoth is what the emitters do: the exact translation, then the approximate one.
func translateBoth(e expr.Expr, from, to expr.Dialect) (expr.Expr, bool) {
	if x, ok := expr.TranslateFacts(e, from, to, expr.Facts{}); ok {
		return x, true
	}
	x, _, ok := expr.TranslateApprox(e, from, to)
	return x, ok
}

func srcCond(op string, l, r expr.Expr) expr.Expr { return bin(op, l, r) }

// TestTagAndPRBranchVariants checks each target result against the source on every run: the target never holds
// where the source does not, and holds exactly where the source does for the runs the harness uses.
func TestTagAndPRBranchVariants(t *testing.T) {
	tagIs := srcCond(expr.OpEq, ref("event"), str("tag"))
	mainIs := srcCond(expr.OpEq, ref("branch"), str("main"))
	conds := map[string]expr.Expr{
		"tag":          tagIs,
		"not tag":      not(tagIs),
		"main":         mainIs,
		"not main":     not(mainIs),
		"tag or main":  bin(expr.OpOr, tagIs, mainIs),
		"tag and main": bin(expr.OpAnd, tagIs, mainIs),
	}
	runs := []run{
		fullRun("push main", "push", "main", "", ""),
		fullRun("push feature", "push", "feature/x", "", ""),
		fullRun("tag", "tag", "", "v1", ""),
		fullRun("pr to main", "pull_request", "main", "", "main"),
		fullRun("pr to dev", "pull_request", "dev", "", "dev"),
		fullRun("manual on a tag ref", "manual", "", "v1", ""),
	}
	froms := map[string]expr.Dialect{"woodpecker": woodpecker.Lang}
	for fname, from := range froms {
		for cname, c := range conds {
			for _, to := range []expr.Dialect{gha.Lang, gitlab.Lang} {
				got, ok := translateBoth(c, from, to)
				if !ok {
					t.Errorf("%s -> %s %s: no translation", fname, to.ID, cname)
					continue
				}
				for _, r := range runs {
					env := r.gha
					if to.ID == gitlab.Lang.ID {
						env = r.gl
					}
					s, g := ev(t, c, r.src, from.Sem), ev(t, got, env, to.Sem)
					if s != g {
						t.Errorf("%s -> %s %s on %s: source %v, target %v (%v)", fname, to.ID, cname, r.name, s, g, got)
					}
				}
			}
		}
	}
}

func TestFullRefVariants(t *testing.T) {
	conds := map[string]expr.Expr{
		"branch main": bin(expr.OpEq, ref("ref"), str("refs/heads/main")),
		"not main":    bin(expr.OpNe, ref("ref"), str("refs/heads/main")),
		"tag prefix":  call("startsWith", ref("ref"), str("refs/tags/")),
		"tag v":       call("startsWith", ref("ref"), str("refs/tags/v")),
		"tag or main": bin(expr.OpOr, call("startsWith", ref("ref"), str("refs/tags/")), bin(expr.OpEq, ref("ref"), str("refs/heads/main"))),
	}
	runs := []run{
		fullRun("push main", "push", "main", "", ""),
		fullRun("push feature", "push", "feature/x", "", ""),
		fullRun("tag", "tag", "", "v1", ""),
		fullRun("pr to main", "pull_request", "main", "", "main"),
	}
	for cname, c := range conds {
		got, ok := translateBoth(c, gha.Lang, gitlab.Lang)
		if !ok {
			t.Errorf("%s: no translation", cname)
			continue
		}
		for _, r := range runs {
			if s, g := ev(t, c, r.gha, gha.Lang.Sem), ev(t, got, r.gl, gitlab.Lang.Sem); s != g {
				t.Errorf("%s on %s: source %v, target %v (%v)", cname, r.name, s, g, got)
			}
		}
	}
}
