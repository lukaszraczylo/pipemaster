package shguard_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/internal/shguard"
)

func TestBaseEnvNamesInvert(t *testing.T) {
	for path, want := range map[string]string{
		"branch": "PM_CTX_BRANCH", "pr.number": "PM_CTX_PR_NUMBER", "ref_name": "PM_CTX_REF_NAME",
		"inputs.deploy-env": "PM_INPUT_DEPLOY_ENV", "matrix.os": "PM_MATRIX_OS",
	} {
		r := expr.Ref{Path: splitDots(path)}
		got, ok := shguard.BaseEnvName(r)
		if !ok || got != want {
			t.Errorf("BaseEnvName(%s) = %q, %v; want %q", path, got, ok, want)
		}
		if back, ok := shguard.BaseEnvRef(got); !ok || !reflect.DeepEqual(back, r) {
			t.Errorf("BaseEnvRef(%q) = %v, %v", got, back, ok)
		}
	}
	for _, p := range [][]string{{"env", "X"}, {"inputs", "Env"}, {"inputs", "a_b"}, {"inputs", "a--b"}, {"needs", "a", "outputs", "b"}, {"nope"}} {
		if n, ok := shguard.BaseEnvName(expr.Ref{Path: p}); ok {
			t.Errorf("BaseEnvName(%v) = %q; want a refusal", p, n)
		}
	}
	for _, n := range []string{"PM_CTX_NOPE", "PM_INPUT_a", "PM_NEEDS_A__B", "HOME"} {
		if r, ok := shguard.BaseEnvRef(n); ok {
			t.Errorf("BaseEnvRef(%q) = %v; want a refusal", n, r)
		}
	}
}

func splitDots(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '.' {
			out, cur = append(out, cur), ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}

func TestEnvVars(t *testing.T) {
	name := func(r expr.Ref) (string, bool) { return shguard.BaseEnvName(r) }
	word := func(r expr.Ref) (string, bool) { return "<" + r.Path[len(r.Path)-1] + ">", true }
	e := expr.Binary{Op: expr.OpAnd,
		L: expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"branch"}}, R: expr.Lit{Kind: expr.LitStr, Str: "a"}},
		R: expr.Unary{Op: expr.OpNot, X: expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"branch"}}, R: expr.Ref{Path: []string{"inputs", "x"}}}},
	}
	got, ok := shguard.EnvVars(e, name, word)
	want := []shguard.EnvVar{{Name: "PM_CTX_BRANCH", Value: "<branch>"}, {Name: "PM_INPUT_X", Value: "<x>"}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("EnvVars = %#v, %v; want %#v", got, ok, want)
	}
	if _, ok := shguard.EnvVars(expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"env", "X"}}, R: expr.Lit{Kind: expr.LitStr}}, name, word); ok {
		t.Error("EnvVars accepted a ref with no env name")
	}
	if _, ok := shguard.EnvVars(expr.Lit{Kind: expr.LitBool}, name, word); ok {
		t.Error("EnvVars accepted an expression with no ref")
	}
	noWord := func(expr.Ref) (string, bool) { return "", false }
	if _, ok := shguard.EnvVars(e, name, noWord); ok {
		t.Error("EnvVars accepted a ref with no word")
	}
}

func TestCaseHelpers(t *testing.T) {
	if got := shguard.UpperUnder("a-b-c"); got != "A_B_C" {
		t.Errorf("UpperUnder = %q", got)
	}
	if got := shguard.LowerDash("A_B_C"); got != "a-b-c" {
		t.Errorf("LowerDash = %q", got)
	}
}
