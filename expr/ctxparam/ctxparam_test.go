package ctxparam_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
)

func TestParam(t *testing.T) {
	for _, c := range []struct {
		want string
		path []string
	}{
		{"ctx-branch", []string{"branch"}},
		{"ctx-pr-number", []string{"pr", "number"}},
		{"ctx-ref-name", []string{"ref_name"}},
		{"ctx-pr-head-ref", []string{"pr", "head_ref"}},
		{"ctx-pr-base-ref", []string{"pr", "base_ref"}},
	} {
		got, ok := ctxparam.Param(c.path)
		if !ok || got != c.want {
			t.Errorf("Param(%v) = %q, %v; want %q", c.path, got, ok, c.want)
		}
		back, ok := ctxparam.Ref(got)
		if !ok || !reflect.DeepEqual(back, c.path) {
			t.Errorf("Ref(%q) = %v, %v; want %v", got, back, ok, c.path)
		}
	}
	for _, bad := range [][]string{{"nope"}, {"inputs", "x"}, {"env", "A"}, nil} {
		if p, ok := ctxparam.Param(bad); ok {
			t.Errorf("Param(%v) = %q; want a refusal", bad, p)
		}
	}
	if _, ok := ctxparam.Ref("ctx-nope"); ok {
		t.Error("Ref accepted an unknown context param")
	}
}

func TestNamesCoverTheCanonicalContext(t *testing.T) {
	want := []string{
		expr.CtxBranch, expr.CtxTag, expr.CtxEvent, expr.CtxSHA, expr.CtxRepo, expr.CtxActor, expr.CtxPRNumber,
		expr.CtxRefName, expr.CtxRefType, expr.CtxRef, expr.CtxPRHeadRef, expr.CtxPRBaseRef,
	}
	if got := ctxparam.Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v; want %v", got, want)
	}
	// The caller must not be able to edit the shared table.
	ctxparam.Names()[0] = "x"
	if ctxparam.Names()[0] != expr.CtxBranch {
		t.Error("Names() exposes the shared table")
	}
}
