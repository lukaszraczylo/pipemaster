package tekton_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/tekton"
)

func TestLangIsRegistered(t *testing.T) {
	d, ok := expr.LangOf(tekton.Dialect)
	if !ok || d.Class != expr.ClassGitLab {
		t.Fatalf("LangOf(tekton) = %+v, %v", d, ok)
	}
}

// A Tekton-origin condition must reach the GHA emitter as a translatable expression, not as an unknown dialect.
func TestTektonConditionTranslatesToGHA(t *testing.T) {
	from := expr.SourceFor(tekton.Dialect, gha.Lang)
	if from == nil || from.Class == 0 {
		t.Fatalf("SourceFor = %+v, want the registered Tekton dialect", from)
	}
	e := tekton.FromWhen([]tekton.Entry{
		{Input: "$(params.ctx-branch)", Operator: tekton.OpIn, Values: []string{"main", "dev"}},
		{Input: "$(params.ctx-event)", Operator: tekton.OpNotIn, Values: []string{"schedule"}},
	})
	got, cav, ok := expr.TranslateApprox(e, *from, gha.Lang)
	if !ok || len(cav) == 0 {
		t.Fatalf("TranslateApprox ok=%v caveats=%v", ok, cav)
	}
	const want = "(((ref_type == 'branch') && (ref_name == 'main')) || ((ref_type == 'branch') && (ref_name == 'dev'))) && (event != 'schedule')"
	if s := expr.String(got); s != want {
		t.Fatalf("translation = %q", s)
	}
}

func TestOpaqueConditionDoesNotTranslate(t *testing.T) {
	from := expr.SourceFor(tekton.Dialect, gha.Lang)
	e := tekton.FromWhen([]tekton.Entry{{CEL: "x"}})
	if _, _, ok := expr.TranslateApprox(e, *from, gha.Lang); ok {
		t.Fatal("an opaque CEL entry must not translate")
	}
}

func TestLangSubsetReadsTektonConditions(t *testing.T) {
	d, _ := expr.LangOf(tekton.Dialect)
	in := tekton.FromWhen([]tekton.Entry{{Input: "$(params.ctx-branch)", Operator: tekton.OpIn, Values: []string{"main"}}})
	if !d.Reads(in) {
		t.Fatal("an in entry is inside the subset")
	}
	notin := tekton.FromWhen([]tekton.Entry{{Input: "$(params.ctx-branch)", Operator: tekton.OpNotIn, Values: []string{"x", "y"}}})
	if !d.Reads(notin) {
		t.Fatal("a notin entry is inside the subset")
	}
}

// A Tekton condition on a pipeline param, a task result or a matrix axis has a GHA spelling, so the other
// providers do not disable it.
func TestTektonInputAndResultConditionsTranslateToGHA(t *testing.T) {
	from := expr.SourceFor(tekton.Dialect, gha.Lang)
	eq := func(path ...string) expr.Expr {
		return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: path}, R: expr.Lit{Kind: expr.LitStr, Str: "README.md"}}
	}
	for _, c := range []struct {
		e    expr.Expr
		want string
	}{
		{eq(expr.PrefixInputs, "path"), "inputs.path == 'README.md'"},
		{eq(expr.PrefixNeeds, "check-file", "outputs", "exists"), "needs.check-file.outputs.exists == 'README.md'"},
		{eq(expr.PrefixMatrix, "go"), "matrix.go == 'README.md'"},
	} {
		got, cav, ok := expr.TranslateApprox(c.e, *from, gha.Lang)
		if !ok || len(cav) == 0 {
			t.Fatalf("%s: TranslateApprox ok=%v caveats=%v", c.want, ok, cav)
		}
		if s := expr.String(got); s != c.want {
			t.Errorf("translation = %q, want %q", s, c.want)
		}
	}
}
