package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestEditedOutputArtifactPathDoesNotRestoreTheUpload(t *testing.T) {
	j, rep := editedParse(t, func(s string) string {
		return replaceOnce(t, s, "path: /workspace/out\n", "path: /workspace/other\n")
	})
	if st := stepByID(j, "up"); st != nil && st.Kind == ir.StepAction {
		t.Errorf("the upload came back although the artifact path was edited")
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}
func TestUneditedJobContainerSurvivesAScriptBeforeASetupAction(t *testing.T) {
	job := &ir.Job{ID: "build", Container: &ir.Image{Ref: "golang:1.22-alpine"}, Steps: []*ir.Step{
		{ID: "pre", Kind: ir.StepScript, Script: []string{"echo pre"}},
		actionStep("go", "actions", "setup-go", map[string]string{"go-version": "1.22"}),
		{ID: "compile", Kind: ir.StepScript, Script: []string{"go build"}},
	}}
	files, _ := emit(t, project(job))
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	got := p.Pipelines[0].Jobs[0]
	if got.Container == nil || got.Container.Ref != "golang:1.22-alpine" {
		t.Errorf("job container = %+v, want golang:1.22-alpine", got.Container)
	}
	if hasEntry(rep, "job image was edited") || hasEntry(rep, "marker ignored") {
		t.Errorf("untruthful entry for an unedited file: %v", rep.Entries)
	}
}

func TestEditedFirstImageKeepsTheFileWhenASetupActionFollows(t *testing.T) {
	job := &ir.Job{ID: "build", Container: &ir.Image{Ref: "golang:1.22-alpine"}, Steps: []*ir.Step{
		{ID: "pre", Kind: ir.StepScript, Script: []string{"echo pre"}},
		actionStep("go", "actions", "setup-go", map[string]string{"go-version": "1.22"}),
		{ID: "compile", Kind: ir.StepScript, Script: []string{"go build"}},
	}}
	files, _ := emit(t, project(job))
	files[ciFile] = replaceOnce(t, files[ciFile], "image: golang:1.22-alpine", "image: golang:1.21-alpine")
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Pipelines[0].Jobs[0]; got.Container != nil && got.Container.Ref == "golang:1.22-alpine" {
		t.Errorf("job container restored although the image was edited: %+v", got.Container)
	}
	if !hasEntry(rep, "job image was edited") {
		t.Errorf("no entry for the edited image: %v", rep.Entries)
	}
}
