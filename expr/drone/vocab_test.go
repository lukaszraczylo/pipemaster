package drone_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/drone"
)

func r(p ...string) expr.Ref                  { return expr.Ref{Path: p} }
func str(s string) expr.Lit                   { return expr.Lit{Kind: expr.LitStr, Str: s} }
func bin(op string, l, x expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: x} }
func not(x expr.Expr) expr.Expr               { return expr.Unary{Op: expr.OpNot, X: x} }
func eq(l expr.Expr, v string) expr.Expr      { return bin(expr.OpEq, l, str(v)) }

func TestVocabMatchesBrief(t *testing.T) {
	for name, want := range map[string][]string{
		"DRONE_BRANCH": {"branch"}, "DRONE_TAG": {"tag"}, "DRONE_BUILD_EVENT": {"event"},
		"DRONE_COMMIT_SHA": {"sha"}, "DRONE_REPO": {"repo"}, "DRONE_COMMIT_AUTHOR": {"actor"},
		"DRONE_PULL_REQUEST": {"pr", "number"}, "DRONE_COMMIT_REF": {"ref"},
	} {
		got := drone.Ref(name)
		if !reflect.DeepEqual(got.Path, want) {
			t.Errorf("Ref(%s) = %v; want %v", name, got.Path, want)
		}
		back, ok := drone.VarName(got)
		if !ok || back != name {
			t.Errorf("VarName(%v) = %q, %v; want %s", got.Path, back, ok, name)
		}
	}
	if got := drone.Ref("DEPLOY_KEY"); !reflect.DeepEqual(got.Path, []string{"env", "DEPLOY_KEY"}) {
		t.Errorf("env ref = %v", got.Path)
	}
}

func TestVarNameRefusesWhatRefCannotReadBack(t *testing.T) {
	for _, p := range [][]string{
		{"pr", "head_ref"}, {"pr", "base_ref"}, {"env", "DRONE_BRANCH"}, {"env", "a b"}, {"env", "1x"}, {"env"},
		{"secrets", "X"}, {"drone", "X"},
	} {
		if v, ok := drone.VarName(expr.Ref{Path: p}); ok {
			t.Errorf("VarName(%v) = %q; want a refusal", p, v)
		}
	}
}

func TestEvents(t *testing.T) {
	want := map[string]string{"pull_request": "pull_request", "schedule": "cron", "workflow_dispatch": "custom"}
	if !reflect.DeepEqual(drone.Events, want) {
		t.Errorf("Events = %v; want %v", drone.Events, want)
	}
	if _, ok := drone.Events["push"]; ok {
		t.Error("push must stay unmapped: a Drone tag push is the tag event")
	}
}

var (
	exact   = expr.Semantics{}
	folding = expr.Semantics{CaseInsensitiveEq: true}
)
