package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func namedProject(name string) *ir.Project {
	return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "gitlab-ci", Name: name, Jobs: []*ir.Job{glJob("build", glStep("make"))}}}}
}

func entriesFor(rep *report.Report, f caps.Feature) map[string]caps.Level {
	got := map[string]caps.Level{}
	for _, e := range rep.Entries {
		if e.Feature == f {
			got[e.IRPath] = e.Level
		}
	}
	return got
}

func TestPipelineNameIsWrittenToWorkflowName(t *testing.T) {
	p := namedProject("Release build")
	files, rep := emitFiles(t, p)
	doc := string(files[rootFile])
	if !strings.Contains(doc, "workflow:\n  name: Release build\n") {
		t.Fatalf("no workflow:name in:\n%s", doc)
	}
	if lvl, ok := entriesFor(rep, caps.DisplayName)["pipeline/gitlab-ci"]; ok && lvl == caps.Lost {
		t.Errorf("pipeline name reported lost: %+v", rep.Entries)
	}
	back, _ := parseFiles(t, map[string]string{rootFile: doc})
	if got := back.Pipelines[0].Name; got != "Release build" {
		t.Errorf("parsed name = %q", got)
	}
}

func TestPipelineNameWithExpressionOrDollarStaysLost(t *testing.T) {
	for _, name := range []string{"build ${{ github.ref_name }}", "price $5", strings.Repeat("x", 256)} {
		files, rep := emitFiles(t, namedProject(name))
		if strings.Contains(string(files[rootFile]), "name:") {
			t.Errorf("name %q must not be written:\n%s", name, files[rootFile])
		}
		if entriesFor(rep, caps.DisplayName)["pipeline/gitlab-ci"] != caps.Lost {
			t.Errorf("name %q must be reported lost: %+v", name, rep.Entries)
		}
	}
}

func TestStepAndJobNamesStayLost(t *testing.T) {
	p := namedProject("")
	j := p.Pipelines[0].Jobs[0]
	j.Name = "Build it"
	j.Steps[0].Name = "Run make"
	_, rep := emitFiles(t, p)
	got := entriesFor(rep, caps.DisplayName)
	if got["pipeline/gitlab-ci/job/build"] != caps.Lost {
		t.Errorf("job name must be lost: %+v", got)
	}
}

func TestSecretEnvIsExternalSetup(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.Env = []ir.Var{{Name: "TOKEN", Value: ir.Value{Kind: ir.ValueSecret, Secret: "DEPLOY_TOKEN"}}}
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "gitlab-ci", Jobs: []*ir.Job{j}}}}
	_, rep := emitFiles(t, p)
	var ext int
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && strings.Contains(e.Detail, "DEPLOY_TOKEN") {
			t.Errorf("secret reported lost: %+v", e)
		}
		if e.Level == caps.External && strings.Contains(e.Strategy+e.Detail, "DEPLOY_TOKEN") {
			ext++
		}
	}
	if ext != 1 {
		t.Errorf("want one external entry naming DEPLOY_TOKEN, got %d: %+v", ext, rep.Entries)
	}
}

func TestTwoSecretsOnOneJobAreBothNamed(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.Env = []ir.Var{
		{Name: "A", Value: ir.Value{Kind: ir.ValueSecret, Secret: "FIRST_TOKEN"}},
		{Name: "B", Value: ir.Value{Kind: ir.ValueSecret, Secret: "SECOND_TOKEN"}},
	}
	_, rep := emitFiles(t, &ir.Project{Pipelines: []*ir.Pipeline{{ID: "gitlab-ci", Jobs: []*ir.Job{j}}}})
	for _, e := range rep.Entries {
		if e.Level == caps.External && strings.Contains(e.Strategy, "FIRST_TOKEN") && strings.Contains(e.Strategy, "SECOND_TOKEN") {
			return
		}
	}
	t.Errorf("no external entry names both secrets: %+v", rep.Entries)
}

func TestServiceSecretSetupStaysVisibleBesideThePortsLoss(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.Services = []ir.Service{{
		Name: "db", Image: ir.Image{Ref: "postgres:16"}, Ports: []string{"5432"},
		Env: []ir.Var{{Name: "PW", Value: ir.Value{Kind: ir.ValueSecret, Secret: "DB_PASSWORD"}}},
	}}
	_, rep := emitFiles(t, &ir.Project{Pipelines: []*ir.Pipeline{{ID: "gitlab-ci", Jobs: []*ir.Job{j}}}})
	var ports, setup bool
	for _, e := range rep.Entries {
		ports = ports || (e.Feature == caps.JobServices && e.Level == caps.Lost && strings.Contains(e.Detail, "ports"))
		setup = setup || (e.Level == caps.External && strings.Contains(e.Strategy, "DB_PASSWORD"))
	}
	if !ports || !setup {
		t.Errorf("ports lost = %v, secret setup named = %v: %+v", ports, setup, rep.Entries)
	}
}

func TestUnwritablePipelineNameSaysWhy(t *testing.T) {
	_, rep := emitFiles(t, namedProject("price $5"))
	for _, e := range rep.Entries {
		if e.Feature == caps.DisplayName && e.IRPath == "pipeline/gitlab-ci" {
			if !strings.Contains(e.Detail, "workflow:name") || strings.Contains(e.Detail, "no display name here") {
				t.Errorf("detail = %q", e.Detail)
			}
			return
		}
	}
	t.Errorf("no display.name entry: %+v", rep.Entries)
}
