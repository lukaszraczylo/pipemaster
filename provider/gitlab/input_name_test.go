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

func TestMultiLineScriptElementKeepsItsInputReferences(t *testing.T) {
	p := inputProject("dep-name")
	p.Pipelines[0].Jobs[0].Steps[0].Script = []string{"bash -c 'echo a\necho ${{ inputs.dep-name }}\necho b'"}
	doc := emitDoc(t, p)
	got, _ := parseFiles(t, map[string]string{rootFile: doc})
	want := []string{"bash -c 'echo a", "echo ${{ inputs.dep-name }}", "echo b'"}
	if s := got.Pipelines[0].Jobs[0].Steps[0].Script; strings.Join(s, "|") != strings.Join(want, "|") {
		t.Errorf("script = %q, want %q\n%s", s, want, doc)
	}
}

func TestReferenceToAnInputWithoutAVariableIsReported(t *testing.T) {
	p := inputProject("a-b", "a_b")
	p.Pipelines[0].Jobs[0].Steps[0].Script = []string{"echo ${{ inputs.a_b }}"}
	doc, rep, err := gitlab.New().Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc[rootFile]), "- echo $a_b") || !hasEntry(rep, "no GitLab variable for expression inputs.a_b") {
		t.Errorf("the reference to the second input aliases the first:\n%s\n%+v", doc[rootFile], rep.Entries)
	}
}
