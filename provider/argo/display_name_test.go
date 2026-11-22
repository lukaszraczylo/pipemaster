package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func namedProject() *ir.Project {
	j := &ir.Job{ID: "build", Name: "Build the binary", Steps: []*ir.Step{
		{ID: "compile", Name: "Compile", Kind: ir.StepScript, Script: []string{"go build ./..."}},
		{ID: "vet", Kind: ir.StepScript, Script: []string{"go vet ./..."}},
	}}
	p := project(j)
	p.Pipelines[0].Name = "Build and test"
	return p
}

func TestDisplayNamesComeBack(t *testing.T) {
	files, _ := emit(t, namedProject())
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	pl := back.Pipelines[0]
	if pl.Name != "Build and test" {
		t.Errorf("pipeline name = %q", pl.Name)
	}
	if pl.Jobs[0].Name != "Build the binary" {
		t.Errorf("job name = %q", pl.Jobs[0].Name)
	}
	if pl.Jobs[0].Steps[0].Name != "Compile" || pl.Jobs[0].Steps[1].Name != "" {
		t.Errorf("step names = %q, %q", pl.Jobs[0].Steps[0].Name, pl.Jobs[0].Steps[1].Name)
	}
	if strings := joinExtP(back); strings != "" && containsTitle(strings) {
		t.Errorf("the title annotation stays in Ext: %s", strings)
	}
	if hasEntry(rep, "marker ignored") {
		t.Errorf("unexpected entry: %v", rep.Entries)
	}
}

func containsTitle(s string) bool { return strings.Contains(s, "workflows.argoproj.io/title") }

func TestAnArgoTitleAnnotationIsThePipelineName(t *testing.T) {
	pl, _ := parseYAML(t, "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata:\n  name: ci\n  annotations:\n    workflows.argoproj.io/title: Nightly\n    keep: me\nspec:\n  entrypoint: main\n  templates:\n  - {name: main, container: {image: x}}\n")
	if pl.Name != "Nightly" {
		t.Errorf("name = %q, want Nightly", pl.Name)
	}
	if s := joinExt(pl.Jobs[0]) + fmtAny(ext(pl.Ext)); containsTitle(s) || !strings.Contains(s, "keep") {
		t.Errorf("Ext = %s, want the annotation keep without the title", s)
	}
}

func TestRemovedJobKeepsNoNameAndReportsTheMarker(t *testing.T) {
	files, _ := emit(t, namedProject())
	f := files[ciFile]
	f = replaceOnce(t, f, "name: build\n            template: build", "name: other\n            template: build")
	files[ciFile] = f
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Pipelines[0].Jobs[0].Name; got != "" {
		t.Errorf("renamed job got the name %q", got)
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}
