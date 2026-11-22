package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// markedProject holds one job and one condition that each carry a marker with no earlier edit test.
func markedProject() *ir.Project {
	j := &ir.Job{ID: "build", Container: &ir.Image{Ref: "golang:1.22", Entrypoint: []string{"/bin/x"}}, Steps: []*ir.Step{
		{ID: "a", Kind: ir.StepScript, Script: []string{"make"}, WorkDir: "sub"},
	}}
	j.Outputs = []ir.Output{{Name: "digest", Value: "abc"}}
	j.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:17", Entrypoint: []string{"/e"}}, Ports: []string{"5432"}}}
	j.Artifacts = []ir.Artifact{{Name: "bin", Paths: []string{"out/bin"}}}
	j.Cache = []ir.Cache{{Key: "k", Paths: []string{"~/.cache"}}}
	d := scriptJob("d", "x")
	d.If = eqStr([]string{"ctx", "branch"}, "main")
	d.Needs = []ir.Need{{JobID: "build"}}
	p := project(j, d)
	p.Pipelines[0].Concurrency = &ir.Concurrency{Group: "ci", CancelInProgress: true}
	return p
}

func editedMarked(t *testing.T, old, repl string) (*ir.Pipeline, *report.Report) {
	t.Helper()
	files, _ := emit(t, markedProject())
	files[ciFile] = replaceOnce(t, files[ciFile], old, repl)
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0], rep
}

func jobOf(pl *ir.Pipeline, id string) *ir.Job {
	for _, j := range pl.Jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func wantIgnored(t *testing.T, rep *report.Report) {
	t.Helper()
	if !hasEntry(rep, "marker ignored") {
		t.Errorf("no marker ignored entry: %v", rep.Entries)
	}
}

func TestEditedWorkdirKeepsTheFileAndIgnoresTheMarker(t *testing.T) {
	pl, rep := editedMarked(t, "workingDir: /workspace/sub", "workingDir: /workspace/other")
	if got := jobOf(pl, "build").Steps[0].WorkDir; got != "/workspace/other" {
		t.Errorf("workdir = %q, want the edited /workspace/other", got)
	}
	wantIgnored(t, rep)
}

func TestEditedNoopContainerKeepsTheFile(t *testing.T) {
	p := project(&ir.Job{ID: "x", Container: &ir.Image{Ref: "alpine:3.22"}})
	files, _ := emit(t, p)
	f := files[ciFile]
	if !strings.Contains(f, "pipemaster:arg") {
		t.Skip("no marker for an empty job")
	}
	files[ciFile] = strings.Replace(f, `- "true"`, `- echo edited`, 1)
	q, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(q.Pipelines[0].Jobs[0].Steps); n != 1 {
		t.Errorf("steps = %d, want the edited container kept", n)
	}
	wantIgnored(t, rep)
}

func TestEditedPipelinePlaceholderTaskKeepsTheFile(t *testing.T) {
	files, _ := emit(t, &ir.Project{Origin: ir.Origin{Provider: "gha", Dialect: "gha"}, Pipelines: []*ir.Pipeline{{ID: "ci"}}})
	f := files[ciFile]
	files[ciFile] = replaceOnce(t, f, `- "true"`, `- echo edited`)
	q, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Pipelines[0].Jobs) != 1 {
		t.Errorf("jobs = %d, want the edited placeholder task kept", len(q.Pipelines[0].Jobs))
	}
	wantIgnored(t, rep)
}
