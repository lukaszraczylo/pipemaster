package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestOutputValueWithAReferenceComesBackWithoutItsEpilogue(t *testing.T) {
	j := scriptJob("version", "make version")
	j.Outputs = []ir.Output{{Name: "tag", Value: "v1-${{ sha }}"}, {Name: "plain", Value: "x"}}
	files, _ := emit(t, project(j))
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Pipelines[0].Jobs[0]
	if len(got.Steps[0].Script) != 1 || len(got.Steps[0].Env) != 0 {
		t.Errorf("script = %v, env = %v; want the user script only", got.Steps[0].Script, got.Steps[0].Env)
	}
	for i, want := range []string{"v1-${{ sha }}", "x"} {
		if got.Outputs[i].Value != want {
			t.Errorf("output %d = %q, want %q", i, got.Outputs[i].Value, want)
		}
	}
	if hasEntry(rep, "marker ignored") {
		t.Errorf("unexpected entry: %v", rep.Entries)
	}
}
