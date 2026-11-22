package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestDownloadActionDoesNotTurnTheNeedIntoAnArtifactNeed(t *testing.T) {
	build := &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "make", Kind: ir.StepScript, Script: []string{"make"}},
		actionStep("up", "actions", "upload-artifact", map[string]string{"name": "dist", "path": "out"}),
	}}
	test := &ir.Job{ID: "test", Needs: []ir.Need{{JobID: "build"}}, Steps: []*ir.Step{
		actionStep("down", "actions", "download-artifact", map[string]string{"name": "dist"}),
		{ID: "run", Kind: ir.StepScript, Script: []string{"ls"}},
	}}
	files, _ := emit(t, project(build, test))
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	var got *ir.Job
	for _, j := range back.Pipelines[0].Jobs {
		if j.ID == "test" {
			got = j
		}
	}
	if got == nil || len(got.Needs) != 1 || got.Needs[0].Artifacts {
		t.Errorf("needs = %+v, want one need without artifacts", got)
	}
}

func TestArtifactNeedStaysWhenTheIRHadIt(t *testing.T) {
	build := &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "make", Kind: ir.StepScript, Script: []string{"make"}},
		actionStep("up", "actions", "upload-artifact", map[string]string{"name": "dist", "path": "out"}),
	}}
	test := &ir.Job{ID: "test", Needs: []ir.Need{{JobID: "build", Artifacts: true}}, Steps: []*ir.Step{
		{ID: "run", Kind: ir.StepScript, Script: []string{"ls"}},
	}}
	files, _ := emit(t, project(build, test))
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range back.Pipelines[0].Jobs {
		if j.ID == "test" && !j.Needs[0].Artifacts {
			t.Errorf("the artifact need was lost: %+v", j.Needs)
		}
	}
}
