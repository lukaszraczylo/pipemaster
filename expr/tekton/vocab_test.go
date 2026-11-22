package tekton_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/tekton"
)

func TestWordRoundTrip(t *testing.T) {
	for _, c := range []struct {
		want string
		path []string
	}{
		{"$(params.ctx-branch)", []string{"branch"}},
		{"$(params.ctx-pr-number)", []string{"pr", "number"}},
		{"$(params.ctx-ref-name)", []string{"ref_name"}},
		{"$(params.tag)", []string{"inputs", "tag"}},
		{"$(tasks.build.results.digest)", []string{"needs", "build", "outputs", "digest"}},
	} {
		got, ok := tekton.RefWord(expr.Ref{Path: c.path})
		if !ok || got != c.want {
			t.Errorf("RefWord(%v) = %q, %v; want %q", c.path, got, ok, c.want)
		}
		back, ok := tekton.FromWord(got)
		if !ok || !reflect.DeepEqual(back.Path, c.path) {
			t.Errorf("FromWord(%q) = %v, %v; want %v", got, back.Path, ok, c.path)
		}
	}
	// A matrix ref prints as a param and reads back as an input: the Task scope decides.
	if w, ok := tekton.RefWord(expr.Ref{Path: []string{"matrix", "os"}}); !ok || w != "$(params.os)" {
		t.Errorf("RefWord(matrix.os) = %q, %v", w, ok)
	}
}

func TestNoWordForSecretsAndEnv(t *testing.T) {
	for _, p := range [][]string{{"env", "X"}, {"secrets", "X"}, {"vars", "X"}, {"steps", "a", "outcome"}, {"nope"}} {
		if w, ok := tekton.RefWord(expr.Ref{Path: p}); ok {
			t.Errorf("RefWord(%v) = %q; want a refusal", p, w)
		}
	}
}

func TestFromWordRefusals(t *testing.T) {
	for _, w := range []string{
		"", "main", "$(params.a) $(params.b)", "$(params.)", "$(params.a b)", "$(context.pipelineRun.name)",
		"$(tasks.a.results.)", "$(tasks.A.results.x)", "$(params.a)x", "x$(params.a)", "$(params.a.b)", "$(params['a'])",
		"$(tasks.status)", "$(tasks.a.status)",
	} {
		if r, ok := tekton.FromWord(w); ok {
			t.Errorf("FromWord(%q) = %v; want a refusal", w, r)
		}
	}
}
