package expr_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
)

func testVocab() expr.Vocab {
	return expr.Vocab{
		ToCanon: map[string][]string{
			"github.ref_name":     {"branch"},
			"github.event.number": {"pr", "number"},
			"matrix":              {"matrix"},
			"secrets":             {"secrets"},
			"needs":               {"needs"},
			"gitlab_matrix":       {"matrix"},
		},
		FromCanon: map[string][]string{
			"branch":    {"github", "ref_name"},
			"pr.number": {"github", "event", "number"},
			"matrix":    {"matrix"},
			"secrets":   {"secrets"},
			"needs":     {"needs"},
		},
	}
}

func TestVocabCanon(t *testing.T) {
	v := testVocab()
	cases := []struct {
		in   []string
		want []string
		ok   bool
	}{
		{[]string{"github", "ref_name"}, []string{"branch"}, true},
		{[]string{"github", "event", "number"}, []string{"pr", "number"}, true},
		{[]string{"matrix", "os"}, []string{"matrix", "os"}, true},
		{[]string{"secrets", "TOKEN"}, []string{"secrets", "TOKEN"}, true},
		{[]string{"needs", "build", "outputs", "v"}, []string{"needs", "build", "outputs", "v"}, true},
		{[]string{"github", "unknown"}, nil, false},
		{[]string{"github", "ref_name", "x"}, nil, false},
		{[]string{"nope"}, nil, false},
		{nil, nil, false},
	}
	for _, c := range cases {
		got, ok := v.Canon(c.in)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("Canon(%v) = %v, %v want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestVocabDialect(t *testing.T) {
	v := testVocab()
	cases := []struct {
		in   []string
		want []string
		ok   bool
	}{
		{[]string{"branch"}, []string{"github", "ref_name"}, true},
		{[]string{"pr", "number"}, []string{"github", "event", "number"}, true},
		{[]string{"matrix", "os"}, []string{"matrix", "os"}, true},
		{[]string{"needs", "b", "outputs", "o"}, []string{"needs", "b", "outputs", "o"}, true},
		{[]string{"tag"}, nil, false},
		{[]string{"branch", "x"}, nil, false},
	}
	for _, c := range cases {
		got, ok := v.Dialect(c.in)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Fatalf("Dialect(%v) = %v, %v want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestVocabDoesNotAliasTables(t *testing.T) {
	v := testVocab()
	got, _ := v.Canon([]string{"matrix", "os"})
	got[0] = "mutated"
	if v.ToCanon["matrix"][0] != "matrix" {
		t.Fatal("Canon must return a fresh slice")
	}
}

func TestCanonicalConstants(t *testing.T) {
	for want, got := range map[string]string{
		"branch": expr.CtxBranch, "tag": expr.CtxTag, "event": expr.CtxEvent, "sha": expr.CtxSHA,
		"repo": expr.CtxRepo, "actor": expr.CtxActor, "pr.number": expr.CtxPRNumber,
		"matrix": expr.PrefixMatrix, "env": expr.PrefixEnv, "secrets": expr.PrefixSecrets,
		"vars": expr.PrefixVars, "inputs": expr.PrefixInputs, "needs": expr.PrefixNeeds,
	} {
		if want != got {
			t.Fatalf("constant %q != %q", got, want)
		}
	}
}
