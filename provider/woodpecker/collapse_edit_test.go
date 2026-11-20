package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const editedSource = `on: push
jobs:
  b:
    runs-on: ubuntu-latest
    continue-on-error: true
    env:
      KEEP: one
    strategy:
      matrix:
        go: ['1.21', '1.22']
        include:
          - go: '1.22'
            experimental: 'true'
    steps:
      - run: echo a
      - run: echo b
`

// editedJob converts the source to Woodpecker, rewrites the file text with edit and parses it back.
func editedJob(t *testing.T, edit func(string) string) (*ir.Job, *report.Report) {
	t.Helper()
	files, _ := emitFiles(t, parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": editedSource}))
	raw := map[string]string{}
	for n, b := range files {
		out := edit(b)
		if out == b {
			t.Fatalf("edit changed nothing in %s:\n%s", n, b)
		}
		raw[n] = out
	}
	src, err := provider.Get("woodpecker")
	if err != nil {
		t.Fatal(err)
	}
	p, rep, err := src.Parse(repo(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0].Jobs[0], rep
}

func ignoredMarker(rep *report.Report, f caps.Feature) bool {
	for _, e := range rep.Entries {
		if e.Feature == f && e.Level == caps.Lost && strings.Contains(e.Detail, "marker ignored") && strings.Contains(e.Detail, "edited after conversion") {
			return true
		}
	}
	return false
}

func TestEditedMatrixKeepsTheParsedValue(t *testing.T) {
	j, rep := editedJob(t, func(s string) string { return strings.Replace(s, `go: "1.21"`, `go: "1.20"`, 1) })
	if j.Matrix == nil || len(j.Matrix.Axes) != 0 {
		t.Fatalf("the marker restored the matrix over the edit: %+v", j.Matrix)
	}
	if got := j.Matrix.Include[0][0].Value; got != "1.20" {
		t.Errorf("first entry = %q, want the edited 1.20", got)
	}
	if !ignoredMarker(rep, caps.JobMatrix) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedAllowFailureKeepsTheParsedValue(t *testing.T) {
	j, rep := editedJob(t, func(s string) string { return strings.Replace(s, "failure: ignore", "", 1) })
	if j.AllowFailure {
		t.Error("the marker restored allow_failure although a step lost failure: ignore")
	}
	if !ignoredMarker(rep, caps.JobAllowFailure) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedEnvKeepsTheParsedValue(t *testing.T) {
	j, rep := editedJob(t, func(s string) string { return strings.ReplaceAll(s, "    environment:\n      KEEP: one\n", "") })
	if len(j.Env) != 0 {
		t.Errorf("the marker restored the job env over the edit: %+v", j.Env)
	}
	if !ignoredMarker(rep, caps.JobEnv) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}
