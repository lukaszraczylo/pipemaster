package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestUneditedCommandWithATagKeepsItsCommandAndArgs(t *testing.T) {
	src := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: app}\nspec:\n  entrypoint: main\n  templates:\n  - name: main\n    inputs: {parameters: [{name: image}]}\n    container:\n      image: x\n      command: [tool]\n      args: [push, '{{inputs.parameters.image}}']\n"
	p, _ := parseFiles(t, map[string]string{"app.yaml": src})
	files, _ := emit(t, p)
	out := files[".argo/workflows/app.yaml"]
	if !strings.Contains(out, "- tool") || !strings.Contains(out, "- push") {
		t.Errorf("command and args lost:\n%s", out)
	}
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}
