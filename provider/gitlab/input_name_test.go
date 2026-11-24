package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

func inputProject(names ...string) *ir.Project {
	pl := &ir.Pipeline{ID: "ci", Jobs: []*ir.Job{{ID: "run", Steps: []*ir.Step{{
		ID: "s", Kind: ir.StepScript, Script: []string{"echo ${{ inputs." + names[0] + " }}"},
	}}}}}
	for _, n := range names {
		pl.Inputs = append(pl.Inputs, ir.Input{Name: n, Default: "d-" + n})
	}
	return &ir.Project{Pipelines: []*ir.Pipeline{pl}}
}

func TestHyphenatedInputIsAVariableAndReturnsByName(t *testing.T) {
	doc := emitDoc(t, inputProject("deployment-name"))
	for _, want := range []string{"deployment_name: d-deployment-name", "echo $deployment_name"} {
		if !strings.Contains(doc, want) {
			t.Errorf("output lacks %q:\n%s", want, doc)
		}
	}
	p, _ := parseFiles(t, map[string]string{rootFile: doc})
	ins := p.Pipelines[0].Inputs
	if len(ins) != 1 || ins[0].Name != "deployment-name" || len(p.Pipelines[0].Env) != 0 {
		t.Fatalf("inputs = %+v, env = %+v", ins, p.Pipelines[0].Env)
	}
}

func TestInputNamesThatShareAVariableAreReported(t *testing.T) {
	_, rep, err := gitlab.New().Emit(inputProject("a-b", "a_b"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntry(rep, "input a_b is not a unique variable name") {
		t.Errorf("no collision entry: %+v", rep.Entries)
	}
}
