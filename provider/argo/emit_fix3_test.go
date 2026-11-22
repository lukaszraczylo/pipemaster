package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	ciFile       = ".argo/workflows/ci.yaml"
	revisionLine = "revision: v4"
	inputsBlock  = "      inputs:\n        artifacts:\n          - name: source\n            path: /workspace\n            git:\n              repo: https://github.com/{{workflow.parameters.ctx-repo}}.git\n              revision: v4\n"
	outputsBlock = "      outputs:\n        artifacts:\n          - name: bin\n            path: /workspace/out\n"
)

func heldJob() *ir.Job {
	return &ir.Job{ID: "build", Container: &ir.Image{Ref: "golang:1.22-alpine"}, Steps: []*ir.Step{
		actionStep("co", "actions", "checkout", map[string]string{"ref": "v4"}),
		actionStep("go", "actions", "setup-go", map[string]string{"go-version": "1.22"}),
		{ID: "compile", Kind: ir.StepScript, Script: []string{"go build"}},
		actionStep("up", "actions", "upload-artifact", map[string]string{"name": "bin", "path": "out"}),
	}}
}

func editedParse(t *testing.T, edit func(string) string) (*ir.Job, *report.Report) {
	t.Helper()
	files, _ := emit(t, project(heldJob()))
	if edit != nil {
		files[ciFile] = edit(files[ciFile])
	}
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0].Jobs[0], rep
}

func stepByID(j *ir.Job, id string) *ir.Step {
	for _, st := range j.Steps {
		if st.ID == id {
			return st
		}
	}
	return nil
}

func replaceOnce(t *testing.T, src, old, repl string) string {
	t.Helper()
	if !strings.Contains(src, old) {
		t.Fatalf("the file has no %q:\n%s", old, src)
	}
	return strings.Replace(src, old, repl, 1)
}

func TestEditedRevisionOfAHeldCheckoutKeepsTheFile(t *testing.T) {
	j, rep := editedParse(t, func(s string) string { return replaceOnce(t, s, revisionLine, "revision: main") })
	var ref string
	for _, st := range j.Steps {
		if st.Action != nil && st.Action.Repo == "checkout" {
			ref = st.With["ref"].Lit
		}
	}
	if ref != "main" {
		t.Errorf("checkout ref = %q, want main; steps %v", ref, j.Steps)
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}

func TestEditedRepoOfAHeldCheckoutKeepsTheFile(t *testing.T) {
	j, rep := editedParse(t, func(s string) string {
		return replaceOnce(t, s, inputsBlock, strings.Replace(inputsBlock, "https://github.com/{{workflow.parameters.ctx-repo}}.git", "https://github.com/x/y.git", 1))
	})
	if stepByID(j, "co") != nil {
		t.Errorf("the held checkout came back although the repo was edited")
	}
	var repo string
	for _, st := range j.Steps {
		if st.Action != nil && st.Action.Repo == "checkout" {
			repo = st.With["repository"].Lit
		}
	}
	if repo != "x/y" {
		t.Errorf("checkout repository = %q, want x/y", repo)
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}

func TestDeletedGitArtifactDoesNotDropARealStep(t *testing.T) {
	j, rep := editedParse(t, func(s string) string { return replaceOnce(t, s, inputsBlock, "") })
	if stepByID(j, "compile") == nil {
		t.Errorf("the compile step was dropped: %v", j.Steps)
	}
	if stepByID(j, "co") != nil {
		t.Errorf("the checkout came back although the artifact was deleted")
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}

func TestEditedImageOfAHeldSetupActionKeepsTheFile(t *testing.T) {
	j, rep := editedParse(t, func(s string) string {
		return strings.ReplaceAll(s, "image: golang:1.22", "image: golang:1.23")
	})
	if stepByID(j, "go") != nil {
		t.Errorf("the setup action came back although the image was edited")
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}

func TestRemovedOutputArtifactDoesNotRestoreTheUpload(t *testing.T) {
	j, rep := editedParse(t, func(s string) string { return replaceOnce(t, s, outputsBlock, "") })
	if stepByID(j, "up") != nil {
		t.Errorf("the upload step came back although the artifact was removed")
	}
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}
