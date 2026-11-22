package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func markedOutputFiles(t *testing.T, edit func(string) string) (*ir.Job, bool) {
	t.Helper()
	j := &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "a", Kind: ir.StepScript, Script: []string{"make"}},
		actionStep("frob", "acme", "frobnicate", nil),
	}, Outputs: []ir.Output{{Name: "digest", Value: "${{ steps.frob.outputs.d }}"}}}
	files, _ := emit(t, project(j))
	if edit != nil {
		files[ciFile] = edit(files[ciFile])
	}
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0].Jobs[0], hasEntry(rep, "marker ignored")
}

func TestMarkedOutputValueComesBackWhenTheFileIsUnedited(t *testing.T) {
	j, ignored := markedOutputFiles(t, nil)
	if j.Outputs[0].Value != "${{ steps.frob.outputs.d }}" || ignored {
		t.Errorf("outputs = %+v, ignored = %v", j.Outputs, ignored)
	}
}

func TestEditedOutputFileDoesNotGetTheMarkedValue(t *testing.T) {
	j, ignored := markedOutputFiles(t, func(s string) string {
		return replaceOnce(t, s, "path: /workspace/.pipemaster/outputs/digest\n", "path: /workspace/.pipemaster/outputs/other\n")
	})
	if j.Outputs[0].Value != "" {
		t.Errorf("output value %q came back although the output file was edited", j.Outputs[0].Value)
	}
	if !ignored {
		t.Error("no marker ignored entry")
	}
}
