package argo_test

import (
	"reflect"
	"testing"
)

// A Workflow named like a WorkflowTemplate must not collide with it.
func TestWorkflowNamedLikeATemplateGetsASuffix(t *testing.T) {
	wt := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: demo}\nspec:\n  entrypoint: main\n  templates:\n  - {name: main, container: {image: x}}\n"
	wf := "apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nmetadata: {generateName: demo-}\nspec:\n  entrypoint: main\n  templates:\n  - {name: main, container: {image: y}}\n"
	p, rep := parseFiles(t, map[string]string{"wt.yaml": wt, "wf.yaml": wf})
	var got []string
	for _, pl := range p.Pipelines {
		got = append(got, pl.ID)
	}
	if !reflect.DeepEqual(got, []string{"demo", "demo-workflow"}) || !hasEntry(rep, "demo-workflow") {
		t.Fatalf("ids %v %+v", got, rep.Entries)
	}
}
