package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	libTemplate = "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: lib}\nspec:\n  templates:\n  - {name: a, container: {image: x}}\n  - {name: b, container: {image: y}}\n"
	runOnly     = "apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nmetadata: {generateName: run-}\nspec:\n  workflowTemplateRef: {name: absent}\n"
)

func TestProjectOfLibraryTemplatesOnlyParsesToAPipelineWithNoJob(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"lib.yaml": libTemplate})
	if len(p.Pipelines) != 1 || len(p.Pipelines[0].Jobs) != 0 {
		t.Fatalf("want one pipeline with no job, got %+v", p.Pipelines)
	}
	if len(entries(rep, "kept in Ext")) == 0 {
		t.Error("no report entry for the kept library")
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

func TestWorkflowWithOnlyATemplateRefKeepsItsDocumentWhenTheTemplateIsAbsent(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"run.yaml": runOnly})
	if len(p.Pipelines) != 1 {
		t.Fatalf("pipelines = %d, want 1", len(p.Pipelines))
	}
	if len(entries(rep, "kind: Workflow")) == 0 {
		t.Error("no report entry for the kept Workflow")
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

func TestTemplateWhoseEntrypointNamesNoTemplateIsALibrary(t *testing.T) {
	dangling := "apiVersion: argoproj.io/v1alpha1\nkind: ClusterWorkflowTemplate\nmetadata: {name: lib}\nspec:\n  entrypoint: gone\n  templates:\n  - {name: a, container: {image: x}}\n"
	p, _ := parseFiles(t, map[string]string{"lib.yaml": dangling})
	if len(p.Pipelines) != 1 || len(p.Pipelines[0].Jobs) != 0 {
		t.Fatalf("want one pipeline with no job, got %+v", p.Pipelines)
	}
}
