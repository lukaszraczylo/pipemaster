package argo_test

import "testing"

func TestClusterWorkflowTemplateWithNoEntrypointIsALibrary(t *testing.T) {
	lib := "apiVersion: argoproj.io/v1alpha1\nkind: ClusterWorkflowTemplate\nmetadata: {name: lib}\nspec:\n  templates:\n  - {name: a, container: {image: x}}\n  - {name: b, container: {image: y}}\n"
	app := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: app}\nspec:\n  entrypoint: main\n  templates:\n  - {name: main, container: {image: x}}\n"
	p, _ := parseFiles(t, map[string]string{"lib.yaml": lib, "app.yaml": app})
	if len(p.Pipelines) != 1 || p.Pipelines[0].ID != "app" {
		t.Errorf("pipelines = %d, want only app", len(p.Pipelines))
	}
}
