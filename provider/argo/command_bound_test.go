package argo_test

import (
	"strings"
	"testing"
)

func TestBoundInputIsNotLeftInTheCommandList(t *testing.T) {
	src := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: app}\nspec:\n  entrypoint: main\n  templates:\n  - name: main\n    dag:\n      tasks:\n      - name: a\n        template: echo\n        arguments: {parameters: [{name: message, value: A}]}\n  - name: echo\n    inputs: {parameters: [{name: message}]}\n    container: {image: x, command: [echo, '{{inputs.parameters.message}}']}\n"
	p, _ := parseFiles(t, map[string]string{"app.yaml": src})
	files, _ := emit(t, p)
	if out := files[".argo/workflows/app.yaml"]; strings.Contains(out, "inputs.parameters.message") {
		t.Errorf("the emitted template reads an input that it does not declare:\n%s", out)
	}
}
