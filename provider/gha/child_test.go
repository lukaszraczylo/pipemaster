package gha_test

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/report"
)

const calledOnly = "on:\n  workflow_call:\njobs:\n  run:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"

func callerYML(uses string) string {
	return "on: push\njobs:\n  call:\n    uses: " + uses + "\n"
}

func callJobOf(t *testing.T, p *ir.Project, pipeline string) *ir.Job {
	t.Helper()
	for _, pl := range p.Pipelines {
		if pl.ID == pipeline {
			for _, j := range pl.Jobs {
				if j.ID == "call" {
					return j
				}
			}
		}
	}
	t.Fatalf("no job call in pipeline %s", pipeline)
	return nil
}

func TestLocalCallBecomesChild(t *testing.T) {
	p, _ := parse(t, map[string]string{"ci.yml": callerYML("./.github/workflows/x.yml"), "x.yml": calledOnly})
	c := callJobOf(t, p, "ci").Child
	if c == nil || c.Pipeline == nil || c.Pipeline.ID != "x" {
		t.Fatalf("child = %+v, want the pipeline x", c)
	}
	if !slices.Equal(c.Files, []string{".github/workflows/x.yml"}) || !slices.Equal(c.Native, []string{"gha"}) {
		t.Errorf("files %v native %v", c.Files, c.Native)
	}
	if len(p.Pipelines) != 1 {
		t.Errorf("a workflow that only workflow_call starts leaves the project pipelines: %d remain", len(p.Pipelines))
	}
}

func TestCalledWorkflowWithOtherTriggersStays(t *testing.T) {
	both := "on:\n  workflow_call:\n  push:\njobs:\n  run:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	p, _ := parse(t, map[string]string{"ci.yml": callerYML("./.github/workflows/x.yml"), "x.yml": both})
	if len(p.Pipelines) != 2 || callJobOf(t, p, "ci").Child == nil {
		t.Fatalf("a workflow that other events start must stay a pipeline and still be the child: %d pipelines", len(p.Pipelines))
	}
}

func TestCallKeepsFailLoudForm(t *testing.T) {
	for _, tc := range []struct{ name, uses string }{
		{"missing local file", "./.github/workflows/none.yml"},
		{"remote", "org/repo/.github/workflows/x.yml@v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := parse(t, map[string]string{"ci.yml": callerYML(tc.uses)})
			j := callJobOf(t, p, "ci")
			if j.Child != nil {
				t.Fatalf("child = %+v, want none", j.Child)
			}
			if len(j.Steps) != 1 || j.Steps[0].Kind != ir.StepPipelineCall {
				t.Errorf("the call step must stay: %+v", j.Steps)
			}
		})
	}
}

func TestCallCycleStops(t *testing.T) {
	a := "on:\n  workflow_call:\njobs:\n  call:\n    uses: ./.github/workflows/b.yml\n"
	b := "on:\n  workflow_call:\njobs:\n  call:\n    uses: ./.github/workflows/a.yml\n"
	p, _ := parse(t, map[string]string{"ci.yml": callerYML("./.github/workflows/a.yml"), "a.yml": a, "b.yml": b})
	c := callJobOf(t, p, "ci").Child
	if c == nil {
		t.Fatal("the first call must attach")
	}
	for depth := 0; c != nil; depth++ {
		if depth > 3 {
			t.Fatal("the child chain does not end: a cycle must stop")
		}
		inner := c.Pipeline.Jobs[0].Child
		c = inner
	}
}

func TestLocalCallIsNativeOnlyForItsOwnDialect(t *testing.T) {
	for _, d := range []gha.Dialect{gha.Gitea, gha.Forgejo, gha.GHA} {
		t.Run(d.ID, func(t *testing.T) {
			dir := map[string]string{gha.Gitea.ID: ".gitea/workflows", gha.Forgejo.ID: ".forgejo/workflows", gha.GHA.ID: ".github/workflows"}[d.ID]
			fsys := fstest.MapFS{
				dir + "/ci.yml": {Data: []byte(callerYML("./" + dir + "/x.yml"))},
				dir + "/x.yml":  {Data: []byte(calledOnly)},
			}
			p, _, err := gha.New(d).Parse(fsys)
			if err != nil {
				t.Fatal(err)
			}
			c := callJobOf(t, p, "ci").Child
			if c == nil || !slices.Equal(c.Native, []string{d.ID}) {
				t.Fatalf("child = %+v, want native only for %s", c, d.ID)
			}
		})
	}
}

func TestNativeEmitWritesTheCalledFileOnce(t *testing.T) {
	p, _ := parse(t, map[string]string{"ci.yml": callerYML("./.github/workflows/x.yml"), "x.yml": calledOnly})
	files, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files[".github/workflows/x.yml"]; !ok {
		t.Fatalf("the called file is not written: %v", keys(files))
	}
	if !strings.Contains(string(files[".github/workflows/ci.yml"]), "uses: ./.github/workflows/x.yml") {
		t.Errorf("the call must stay a job-level uses:\n%s", files[".github/workflows/ci.yml"])
	}
	if slices.ContainsFunc(rep.Entries, func(e report.Entry) bool { return e.Level == caps.Emulated && strings.Contains(e.Detail, "inline") }) {
		t.Errorf("a native call needs no inline entry: %v", rep.Entries)
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
