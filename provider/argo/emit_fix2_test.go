package argo_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func envVars(pairs ...string) []ir.Var {
	var out []ir.Var
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, ir.Var{Name: pairs[i], Value: ir.Value{Lit: pairs[i+1]}})
	}
	return out
}

func ignoredMarkers(rep *report.Report) []string {
	var out []string
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "marker ignored") {
			out = append(out, e.Detail)
		}
	}
	return out
}

func unmappedStep(id string) *ir.Step {
	return &ir.Step{ID: id, Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Host: "https://github.com", Owner: "acme", Repo: "unmapped", Ref: "v1"}}
}

func TestEnvLiftsFromJobsThatMixActionsAndScripts(t *testing.T) {
	mixed := scriptJob("mixed", "echo one")
	mixed.Steps = append([]*ir.Step{unmappedStep("setup")}, mixed.Steps...)
	mixed.Env = envVars("JOB_VAR", "j")
	onlyAction := &ir.Job{ID: "only", Steps: []*ir.Step{unmappedStep("solo")}, Env: envVars("ONLY_VAR", "o")}
	p := project(mixed, onlyAction, scriptJob("plain", "echo two"))
	p.Pipelines[0].Env = envVars("PIPE_VAR", "p")
	files, _ := emit(t, p)
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	pl := back.Pipelines[0]
	if !reflect.DeepEqual(pl.Env, p.Pipelines[0].Env) {
		t.Errorf("pipeline env = %v, want %v", pl.Env, p.Pipelines[0].Env)
	}
	for i, j := range pl.Jobs {
		if want := p.Pipelines[0].Jobs[i].Env; !reflect.DeepEqual(j.Env, want) {
			t.Errorf("job %s env = %v, want %v", j.ID, j.Env, want)
		}
		for _, st := range j.Steps {
			if len(st.Env) > 0 {
				t.Errorf("job %s step %s holds env %v: the lift left the variables in the steps", j.ID, st.ID, st.Env)
			}
		}
	}
	if got := ignoredMarkers(rep); len(got) > 0 {
		t.Errorf("the markers were ignored although nothing was edited: %v", got)
	}
}

func TestPipelineWithNoJobKeepsItsEnv(t *testing.T) {
	p := project()
	p.Pipelines[0].Env = envVars("KEEP", "me")
	files, _ := emit(t, p)
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	pl := back.Pipelines[0]
	if !reflect.DeepEqual(pl.Env, p.Pipelines[0].Env) {
		t.Errorf("pipeline env = %v, want %v", pl.Env, p.Pipelines[0].Env)
	}
	if len(pl.Jobs) != 0 {
		t.Errorf("the placeholder task came back as a job: %v", pl.Jobs)
	}
	if got := ignoredMarkers(rep); len(got) > 0 {
		t.Errorf("the markers were ignored although nothing was edited: %v", got)
	}
}
