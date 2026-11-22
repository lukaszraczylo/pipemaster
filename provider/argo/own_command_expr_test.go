package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestCommandWithADeclaredInputTagRoundTrips(t *testing.T) {
	src := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: app}\nspec:\n  entrypoint: main\n  templates:\n  - name: main\n    inputs: {parameters: [{name: image}]}\n    container:\n      image: x\n      command: [tool]\n      args: [push, '{{inputs.parameters.image}}']\n"
	p, _ := parseFiles(t, map[string]string{"app.yaml": src})
	files, _ := emit(t, p)
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}
