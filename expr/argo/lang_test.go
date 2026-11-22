package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/argo"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
)

// Lang is not registered here, so the test passes it to TranslateApprox directly.
func TestArgoConditionTranslatesToGHA(t *testing.T) {
	e := argo.FromWhen("{{workflow.parameters.ctx-branch}} == main && {{workflow.parameters.ctx-event}} != schedule", argo.ScopeTask)
	got, cav, ok := expr.TranslateApprox(e, argo.Lang, gha.Lang)
	if !ok || len(cav) == 0 {
		t.Fatalf("TranslateApprox ok=%v caveats=%v", ok, cav)
	}
	const want = "(ref_type == 'branch') && (ref_name == 'main') && (event != 'schedule')"
	if s := expr.String(got); s != want {
		t.Fatalf("translation = %q, want %q", s, want)
	}
}

func TestArgoInputNeedsAndMatrixConditionsTranslateToGHA(t *testing.T) {
	for _, c := range []struct {
		when string
		want string
	}{
		{"{{= workflow.parameters['path'] == 'README.md' }}", "inputs.path == 'README.md'"},
		{"{{= tasks['check-file'].outputs.parameters['exists'] == 'README.md' }}", "needs.check-file.outputs.exists == 'README.md'"},
		{"{{= item['go'] == 'README.md' }}", "matrix.go == 'README.md'"},
	} {
		got, cav, ok := expr.TranslateApprox(argo.FromWhen(c.when, argo.ScopeTask), argo.Lang, gha.Lang)
		if !ok || len(cav) == 0 {
			t.Fatalf("%s: TranslateApprox ok=%v caveats=%v", c.when, ok, cav)
		}
		if s := expr.String(got); s != c.want {
			t.Errorf("translation = %q, want %q", s, c.want)
		}
	}
}

func TestOpaqueArgoConditionDoesNotTranslate(t *testing.T) {
	e := argo.FromWhen("{{= workflow.parameters['ctx-branch'] =~ '^v' }}", argo.ScopeTask)
	if _, _, ok := expr.TranslateApprox(e, argo.Lang, gha.Lang); ok {
		t.Fatal("an opaque Argo condition must not translate")
	}
}

func TestLangSubset(t *testing.T) {
	for when, want := range map[string]bool{
		"{{= workflow.parameters['ctx-branch'] == 'main' }}":                                       true,
		"{{= !(workflow.parameters['ctx-branch'] != 'main') || workflow.parameters['x'] == 'y' }}": true,
		"{{= workflow.parameters['ctx-branch'] == '' }}":                                           false,
		"{{= workflow.parameters['n'] == 3 }}":                                                     false,
		"{{= workflow.parameters['ctx-branch'] =~ '^v' }}":                                         false,
		"{{= false }}": false,
	} {
		if got := argo.Lang.Reads(argo.FromWhen(when, argo.ScopeTask)); got != want {
			t.Errorf("Reads(%q) = %v, want %v", when, got, want)
		}
	}
	if !argo.Lang.Supports(argo.FromWhen("{{= workflow.parameters['ctx-branch'] == 'main' }}", argo.ScopeTask)) {
		t.Error("Supports refused a printable condition")
	}
	if argo.Lang.Supports(expr.Call{Name: "success"}) {
		t.Error("Supports accepted a call")
	}
}
