package expr_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
)

func TestRefTypeForm(t *testing.T) {
	ref := expr.Ref{Path: []string{expr.CtxRef}}
	starts := func(prefix string) expr.Expr {
		return expr.Call{Name: "startsWith", Args: []expr.Expr{ref, expr.Lit{Kind: expr.LitStr, Str: prefix}}}
	}
	isTag := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxRefType}}, R: expr.Lit{Kind: expr.LitStr, Str: "tag"}}
	other := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: expr.Lit{Kind: expr.LitStr, Str: "main"}}
	byName := expr.Call{Name: "startsWith", Args: []expr.Expr{expr.Ref{Path: []string{expr.CtxRefName}}, expr.Lit{Kind: expr.LitStr, Str: "refs/tags/"}}}
	cases := []struct {
		in, want expr.Expr
		name     string
	}{
		{starts("refs/tags/"), isTag, "tag prefix"},
		{expr.Unary{Op: expr.OpNot, X: starts("refs/tags/")}, expr.Unary{Op: expr.OpNot, X: isTag}, "negated tag prefix"},
		{expr.Binary{Op: expr.OpAnd, L: starts("refs/tags/"), R: other}, expr.Binary{Op: expr.OpAnd, L: isTag, R: other}, "inside and"},
		{starts("refs/heads/"), starts("refs/heads/"), "branch prefix stays, a pull request has the ref type branch"},
		{starts("refs/tags/v"), starts("refs/tags/v"), "a longer tag prefix stays"},
		{byName, byName, "another ref stays"},
		{nil, nil, "nil"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expr.RefTypeForm(c.in)
			if (got == nil) != (c.want == nil) || got != nil && expr.String(got) != expr.String(c.want) {
				t.Errorf("RefTypeForm = %v, want %v", got, c.want)
			}
		})
	}
}
