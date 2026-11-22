package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestMultilineCommandArgumentReadsAsOneIRLinePerScriptLine(t *testing.T) {
	src := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: app}\nspec:\n  entrypoint: main\n  templates:\n  - name: main\n    container:\n      image: x\n      command: [sh, -c]\n      args: [\"echo a\\necho b\"]\n"
	p, _ := parseFiles(t, map[string]string{"app.yaml": src})
	s := p.Pipelines[0].Jobs[0].Steps[0]
	if len(s.Script) != 2 {
		t.Fatalf("script = %q", s.Script)
	}
	files, _ := emit(t, p)
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}
