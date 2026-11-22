package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const bareDepends = "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: app}\nspec:\n  entrypoint: main\n  templates:\n  - name: main\n    dag:\n      tasks:\n      - {name: a, template: t}\n      - {name: b, template: t, depends: a}\n  - {name: t, container: {image: x}}\n"

func emitDependsOf(t *testing.T, p *ir.Project) string {
	t.Helper()
	files, _ := emit(t, p)
	for _, l := range strings.Split(files[".argo/workflows/app.yaml"], "\n") {
		if strings.Contains(l, "depends:") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func TestUneditedBareDependsIsWrittenAsItWas(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"app.yaml": bareDepends})
	if got := emitDependsOf(t, p); got != "depends: a" {
		t.Errorf("got %q, want the text of the file", got)
	}
}

func TestEditedConditionDropsTheKeptDependsText(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"app.yaml": bareDepends})
	p.Pipelines[0].Jobs[1].If = nil
	got := emitDependsOf(t, p)
	if got == "depends: a" {
		t.Errorf("kept the old text after the condition changed")
	}
}
