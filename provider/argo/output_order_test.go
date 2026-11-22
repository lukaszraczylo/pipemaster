package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestEveryOutputEpilogueComesBackOut(t *testing.T) {
	j := scriptJob("version", "make version")
	j.Outputs = []ir.Output{{Name: "first", Value: "a"}, {Name: "second", Value: "b"}}
	files, _ := emit(t, project(j))
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Pipelines[0].Jobs[0]
	if len(got.Steps[0].Script) != 1 || len(got.Steps[0].Env) != 0 {
		t.Errorf("script = %v, env = %v; want the user script only", got.Steps[0].Script, got.Steps[0].Env)
	}
}
