package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func actionStep(id, owner, repo string, with map[string]string) *ir.Step {
	w := map[string]ir.Value{}
	for k, v := range with {
		w[k] = ir.Value{Lit: v}
	}
	return &ir.Step{ID: id, Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: owner, Repo: repo, Ref: "v4"}, With: w}
}

func actionProject() *ir.Project {
	build := &ir.Job{ID: "build", Steps: []*ir.Step{
		actionStep("co", "actions", "checkout", map[string]string{"fetch-depth": "1"}),
		actionStep("go", "actions", "setup-go", map[string]string{"go-version": "1.27"}),
		{ID: "run", Kind: ir.StepScript, Script: []string{"go build -o out/bin ./..."}},
		actionStep("up", "actions", "upload-artifact", map[string]string{"name": "bin", "path": "out/bin"}),
	}}
	test := &ir.Job{ID: "test", Needs: []ir.Need{{JobID: "build"}}, Steps: []*ir.Step{
		actionStep("down", "actions", "download-artifact", map[string]string{"name": "bin", "path": "dl"}),
		{ID: "run", Kind: ir.StepScript, Script: []string{"ls dl"}},
	}}
	return project(build, test)
}

func TestActionsBecomeGitArtifactImageAndArtifacts(t *testing.T) {
	files, rep := emit(t, actionProject())
	all := files[".argo/workflows/ci.yaml"]
	for _, want := range []string{"git:", "https://github.com/{{workflow.parameters.ctx-repo}}.git", "{{workflow.parameters.ctx-sha}}", "golang:1.27",
		"outputs:", "artifacts:", "{{tasks.build.outputs.artifacts.bin}}", "depth: 1"} {
		if !strings.Contains(all, want) {
			t.Errorf("workflow lacks %q:\n%s", want, all)
		}
	}
	if len(entries(rep, "private repository")) == 0 {
		t.Error("the private repository credentials are not reported")
	}
	back := roundTrip(t, files)
	if got := back.Pipelines[0].Jobs[0].Steps[0]; got.Kind != ir.StepAction || got.Action == nil {
		t.Errorf("the checkout step came back as %+v", got)
	}
}

func TestUnknownActionIsLostWithItsReference(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{actionStep("x", "acme", "thing", nil), {ID: "run", Kind: ir.StepScript, Script: []string{"echo"}}}}
	files, rep := emit(t, project(j))
	if !strings.Contains(files[".argo/workflows/ci.yaml"], "acme/thing@v4") {
		t.Errorf("the action reference is not kept as a comment:\n%s", files[".argo/workflows/ci.yaml"])
	}
	if len(entries(rep, "no Argo mapping")) == 0 {
		t.Error("no lost entry")
	}
}

func TestActionProjectPassesArgoLint(t *testing.T) {
	linttool.RequireArgo(t)
	files, _ := emit(t, actionProject())
	if out, err := linttool.ArgoLint(t, ".argo/workflows/ci.yaml", []byte(files[".argo/workflows/ci.yaml"])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[".argo/workflows/ci.yaml"])
	}
}
