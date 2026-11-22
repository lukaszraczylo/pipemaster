package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func tripEqual(t *testing.T, p *ir.Project) map[string]string {
	t.Helper()
	files, _ := emit(t, p)
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("the trip changed the IR:\n%s\n%s", diff, joinFiles(files))
	}
	return files
}

func TestInputsOfEveryTypeBecomeStringsAndComeBack(t *testing.T) {
	j := scriptJob("build", "echo ${{ inputs.flag }} ${{ inputs.ctx-branch }}")
	p := project(j)
	p.Pipelines[0].Inputs = []ir.Input{
		{Name: "flag", Type: "boolean", Default: "true"},
		{Name: "ctx-branch", Default: "dev"},
		{Name: "needed", Required: true},
	}
	files := tripEqual(t, p)
	args := list(get(template(t, files), "spec", "arguments", "parameters"))
	names := map[string]bool{}
	for _, a := range args {
		names[a.(doc)["name"].(string)] = true
	}
	if !names["flag"] || !names["input-ctx-branch"] || !names["needed"] {
		t.Errorf("arguments = %v", names)
	}
	if v := named(t, args, "needed")["value"]; v != nil {
		t.Errorf("a required input has the value %v", v)
	}
}

func TestEditedInputMarkerIsIgnored(t *testing.T) {
	p := project(scriptJob("build", "echo ${{ inputs.ctx-branch }}"))
	p.Pipelines[0].Inputs = []ir.Input{{Name: "ctx-branch", Default: "dev"}}
	files, _ := emit(t, p)
	for n, s := range files {
		files[n] = strings.ReplaceAll(s, "name: input-ctx-branch", "name: renamed")
	}
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if in := back.Pipelines[0].Inputs; len(in) != 1 || in[0].Name != "renamed" {
		t.Errorf("inputs = %+v, want the edited name kept", in)
	}
	if len(entries(rep, "marker ignored")) == 0 {
		t.Error("no marker ignored entry")
	}
}

func TestStepConditionsAndContinueOnErrorComeBack(t *testing.T) {
	j := scriptJob("build", "echo a", "echo ${{ branch }}")
	j.Steps[0].If = eqStr([]string{"branch"}, "main")
	j.Steps[0].ContinueOnError = true
	tripEqual(t, project(j))
}

func TestScriptVariablesComeBack(t *testing.T) {
	j := scriptJob("build", "echo ${{ sha }} ${{ env.A }} ${{ secrets.TOK }} ${{ vars.V }}")
	j.Env = nil
	files := tripEqual(t, project(j))
	if s := scripts(t, files); len(s) != 1 || strings.Contains(s[0], "{{") {
		t.Errorf("script = %q", s)
	}
}

func TestEditedScriptVariableEnvIsKeptAndMarkerIgnored(t *testing.T) {
	files, _ := emit(t, project(scriptJob("build", "echo ${{ sha }}")))
	for n, s := range files {
		files[n] = strings.Replace(s, "'{{workflow.parameters.ctx-sha}}'", "edited", 1)
	}
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if len(st.Script) != 1 || !strings.Contains(st.Script[0], "${PM_CTX_SHA}") {
		t.Errorf("script = %v, want the text kept", st.Script)
	}
	if len(st.Env) != 1 || st.Env[0].Value.Lit != "edited" {
		t.Errorf("env = %+v, want the edited value kept", st.Env)
	}
	if len(entries(rep, "marker ignored")) == 0 {
		t.Error("no marker ignored entry")
	}
}

func TestJobOutputsWithValuesComeBack(t *testing.T) {
	j := scriptJob("build", "echo hi")
	j.Outputs = []ir.Output{{Name: "a", Value: "x y"}, {Name: "b"}}
	files := tripEqual(t, project(j))
	if !strings.Contains(joinFiles(files), "name: main") {
		t.Errorf("no container named main:\n%s", joinFiles(files))
	}
}

func TestStepIDsSurviveContainerNames(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "Step One", Kind: ir.StepScript, Script: []string{"a"}},
		{ID: "step_two", Kind: ir.StepScript, Script: []string{"b"}},
	}}
	tripEqual(t, project(j))
}

func TestServicesTimeoutRetryAllowFailure(t *testing.T) {
	j := scriptJob("build", "x")
	d := ir.Duration(90_000_000_000)
	j.Timeout, j.Retry, j.AllowFailure = &d, &ir.Retry{Attempts: 3}, true
	j.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:17"}, Ports: []string{"5432"}, Env: []ir.Var{{Name: "A", Value: ir.Value{Lit: "b"}}}}}
	tripEqual(t, project(j))
}

func TestPipelineFinallyAndConcurrency(t *testing.T) {
	p := project(scriptJob("a", "x"))
	p.Pipelines[0].Finally = []*ir.Job{scriptJob("done", "y")}
	p.Pipelines[0].Concurrency = &ir.Concurrency{Group: "ci"}
	files := tripEqual(t, p)
	if !strings.Contains(joinFiles(files), "exit-handler") {
		t.Error("no exit handler")
	}
}
