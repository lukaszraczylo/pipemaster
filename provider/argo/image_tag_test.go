package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestImageTagOfAnArgoFileIsWrittenBack(t *testing.T) {
	src := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: app}\nspec:\n  entrypoint: main\n  templates:\n  - name: main\n    inputs: {parameters: [{name: img}]}\n    container:\n      image: '{{inputs.parameters.img}}'\n      command: [sh, -c]\n      args: [uname]\n"
	p, _ := parseFiles(t, map[string]string{"app.yaml": src})
	files, _ := emit(t, p)
	if out := files[".argo/workflows/app.yaml"]; !strings.Contains(out, "image: '{{inputs.parameters.img}}'") && !strings.Contains(out, "image: \"{{inputs.parameters.img}}\"") {
		t.Errorf("the image tag is lost:\n%s", out)
	}
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}

func TestImageTagFromAnotherProviderStaysLost(t *testing.T) {
	j := scriptJob("a", "true")
	j.Container = &ir.Image{Ref: "{{workflow.name}}"}
	files, rep := emit(t, project(j))
	if strings.Contains(files[".argo/workflows/ci.yaml"], "{{workflow.name}}") || !hasEntry(rep, "default image") {
		t.Errorf("a tag from another provider reached the image: %v", rep.Entries)
	}
}
