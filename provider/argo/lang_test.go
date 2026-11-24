package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprargo "github.com/lukaszraczylo/pipemaster/expr/argo"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/argo"
)

func TestLangIsRegisteredAndSourceForFindsIt(t *testing.T) {
	d, ok := expr.LangOf("argo")
	if !ok || d.ID != exprargo.Dialect {
		t.Fatalf("LangOf(argo) = %v, %v", d, ok)
	}
	if from := expr.SourceFor("argo", exprgha.Lang); from == nil || from.ID != "argo" || from.Class == 0 {
		t.Fatalf("SourceFor(argo, gha) = %+v", from)
	}
}

// An Argo condition read by the parser translates to GHA with inputs, needs outputs and matrix refs.
func TestArgoConditionTranslatesToGHA(t *testing.T) {
	pl, _ := parseYAML(t, dagOf(`      - {name: build, template: echo}
      - {name: a, template: echo, dependencies: [build], when: "{{workflow.parameters.ctx-branch}} == main && {{workflow.parameters.env}} != prod"}
      - {name: b, template: echo, dependencies: [build], when: "{{tasks.build.outputs.parameters.version}} == v1"}
      - {name: c, template: echo, when: "{{= workflow.parameters['ctx-event'] == 'push' }}"}
`, echoTemplates))
	from := expr.SourceFor("argo", exprgha.Lang)
	for id, want := range map[string]string{
		"a": "(ref_type == 'branch') && (ref_name == 'main') && (inputs.env != 'prod')",
		"b": "needs.build.outputs.version == 'v1'",
		"c": "(event == 'push') && !deleted",
	} {
		j := job(t, pl, id)
		got, _, ok := expr.TranslateApprox(j.If, *from, exprgha.Lang)
		if !ok {
			t.Fatalf("%s: no translation for %s", id, expr.String(j.If))
		}
		if s := expr.String(got); s != want {
			t.Errorf("%s: GHA %q, want %q", id, s, want)
		}
	}
}

// The parser keeps loops in Ext, but a matrix axis read from a loop item still translates.
func TestArgoMatrixConditionTranslatesToGHA(t *testing.T) {
	from := expr.SourceFor("argo", exprgha.Lang)
	got, _, ok := expr.TranslateApprox(exprargo.FromWhen("{{item.os}} == linux", exprargo.ScopeTask), *from, exprgha.Lang)
	if !ok {
		t.Fatal("no translation")
	}
	if s := expr.String(got); s != "matrix.os == 'linux'" {
		t.Fatalf("GHA %q", s)
	}
}
