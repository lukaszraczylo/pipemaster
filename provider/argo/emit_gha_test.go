package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider/argo"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

const ghaCtxInput = `name: ci
on:
  workflow_dispatch:
    inputs:
      ctx-branch:
        default: dev
        description: a name that the Argo context also uses
      flag:
        type: boolean
        default: true
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo ${{ inputs.ctx-branch }} ${{ inputs.flag }}
`

// TestGHAInputNamedLikeAContextParamSurvivesArgo walks GHA to Argo and back, with an input that the context vocabulary reserves.
func TestGHAInputNamedLikeAContextParamSurvivesArgo(t *testing.T) {
	g := gha.New(gha.GHA)
	first, _, err := g.Parse(repo(map[string]string{".github/workflows/ci.yml": ghaCtxInput}))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := argo.New().Emit(first)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for n, b := range raw {
		files[n] = string(b)
	}
	mid, _, err := argo.New().Parse(repo(files))
	if err != nil {
		t.Fatalf("%v\n%s", err, joinFiles(files))
	}
	var names []string
	for _, in := range mid.Pipelines[0].Inputs {
		names = append(names, in.Name+":"+in.Type)
	}
	if got := strings.Join(names, ","); got != "ctx-branch:,flag:boolean" {
		t.Errorf("inputs after Argo = %s", got)
	}
	out, _, err := g.Emit(mid)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out[".github/workflows/ci.yml"])
	for _, want := range []string{"ctx-branch:", "inputs.ctx-branch", "inputs.flag"} {
		if !strings.Contains(text, want) {
			t.Errorf("GHA output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "input-ctx-branch") {
		t.Errorf("the rename leaked into GHA:\n%s", text)
	}
}
