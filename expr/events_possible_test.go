package expr_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

func TestFoldEvent(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"parent source holds", `$CI_PIPELINE_SOURCE == "parent_pipeline"`, "true"},
		{"push source fails", `$CI_PIPELINE_SOURCE == "push"`, "false"},
		{"negated push holds", `$CI_PIPELINE_SOURCE != "push"`, "true"},
		{"other test stays", `$CI_PIPELINE_SOURCE == "parent_pipeline" && $FLAG == "1"`, "gitlab.FLAG == '1'"},
		{"or with push", `$CI_PIPELINE_SOURCE == "push" || $CI_PIPELINE_SOURCE == "parent_pipeline"`, "true"},
		{"and with push", `$CI_PIPELINE_SOURCE == "push" && $FLAG == "1"`, "false"},
		{"no event test", `$FLAG == "1"`, "gitlab.FLAG == '1'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := exprgitlab.Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			got := expr.String(expr.FoldEvent(e, "parent_pipeline"))
			if got != tc.want {
				t.Fatalf("FoldEvent = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEventsPossibleTagPush(t *testing.T) {
	event, refType := ref(expr.CtxEvent), ref(expr.CtxRefType)
	for _, tc := range []struct {
		e    expr.Expr
		name string
		want bool
	}{
		{bin(expr.OpEq, event, str("push")), "push event", true},
		{bin(expr.OpEq, event, str("tag")), "tag event", true},
		{bin(expr.OpEq, event, str("pull_request")), "pull request event", false},
		{bin(expr.OpNe, event, str("push")), "not push event", false},
		{bin(expr.OpEq, refType, str("tag")), "tag ref type", true},
		{bin(expr.OpEq, refType, str("branch")), "branch ref type", false},
		{bin(expr.OpNe, refType, str("tag")), "not tag ref type", false},
		{bin(expr.OpMatch, event, str("(?i)^pu")), "push pattern", true},
		{bin(expr.OpMatch, event, str("(?i)^pull")), "pull pattern", false},
		{bin(expr.OpNoMatch, event, str("(?i)^push$")), "not push pattern", false},
		{bin(expr.OpAnd, bin(expr.OpEq, event, str("push")), bin(expr.OpEq, refType, str("branch"))), "push on a branch", false},
		{bin(expr.OpOr, bin(expr.OpEq, event, str("push")), bin(expr.OpEq, refType, str("branch"))), "push or branch", true},
		{bin(expr.OpEq, ref(expr.CtxRefName), str("main")), "unknown test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := len(expr.EventsPossible(tc.e, []string{expr.TagPush})) > 0
			if got != tc.want {
				t.Errorf("EventsPossible(%s) on a tag push = %v, want %v", expr.String(tc.e), got, tc.want)
			}
		})
	}
}
