package expr_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

func TestTranslateRefs(t *testing.T) {
	ref := func(p ...string) expr.Expr { return expr.Ref{Path: p} }
	str := func(s string) expr.Expr { return expr.Lit{Kind: expr.LitStr, Str: s} }
	format := func(args ...expr.Expr) expr.Expr { return expr.Call{Name: "format", Args: args} }
	cases := []struct {
		in   expr.Expr
		name string
		want bool
	}{
		{format(str("{0}-{1}"), ref(expr.CtxSHA), ref(expr.CtxRefName)), "all refs known", true},
		{ref(expr.CtxSHA), "single known ref", true},
		{format(str("img:{0}"), ref(expr.CtxSHA)), "literal text around a ref", true},
		{format(str("{0}-{1}"), ref(expr.CtxSHA), ref("no_such_name")), "one ref unknown to the dialects", false},
		{format(str("{0}"), ref(expr.CtxBranch)), "ref only the source dialect names", false},
		{ref(expr.CtxEvent), "event ref is refused", false},
		{format(str("$CI_REGISTRY_IMAGE/tmp:{0}"), ref(expr.CtxSHA)), "literal text holds a variable", false},
		{expr.Binary{Op: expr.OpEq, L: ref(expr.CtxSHA), R: str("x")}, "not a reference-only value", false},
		{expr.Call{Name: "contains", Args: []expr.Expr{ref(expr.CtxSHA), str("a")}}, "unknown call", false},
		{expr.Ref{}, "empty path", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := expr.TranslateRefs(c.in, gitlab.Lang, gha.Lang)
			if ok != c.want {
				t.Fatalf("ok = %v, want %v", ok, c.want)
			}
			if ok && expr.String(got) != expr.String(c.in) {
				t.Errorf("translated %s to %s; a reference keeps its canonical name", expr.String(c.in), expr.String(got))
			}
		})
	}
	if _, ok := expr.TranslateRefs(ref(expr.CtxSHA), expr.Dialect{}, gha.Lang); ok {
		t.Error("an empty source dialect must not translate")
	}
}
