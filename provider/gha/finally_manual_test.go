package gha_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

// A manual finally job keeps both guards in GHA and returns as a manual finally job, so nothing is dropped.
func TestManualFinallyJobKeepsBothGuards(t *testing.T) {
	step := []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"x"}}}
	runner := ir.Runner{Labels: []string{"r"}}
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci",
		Triggers: []ir.Trigger{{Kind: ir.TriggerPush}},
		Jobs:     []*ir.Job{{ID: "a", Runner: runner, Steps: step}},
		Finally:  []*ir.Job{{ID: "f", Runner: runner, Steps: step, Manual: true}},
	}}}
	a := gha.New(gha.GHA)
	files, rep, err := a.Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for n, d := range files {
		fsys[n] = &fstest.MapFile{Data: d}
		if !strings.Contains(string(d), "if: always() && github.event_name == 'workflow_dispatch'") {
			t.Errorf("%s lacks the combined guard:\n%s", n, d)
		}
	}
	seen := map[caps.Feature]bool{}
	for _, e := range rep.Entries {
		seen[e.Feature] = e.Level == caps.Emulated
	}
	if !seen[caps.PipelineFinally] || !seen[caps.JobManual] {
		t.Errorf("want emulated entries for pipeline.finally and job.manual, have %v", seen)
	}
	back, _, err := a.Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ir.CoreOf(p)
	got, err := ir.CoreOf(back)
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(want, got); d != "" {
		t.Errorf("round trip differs (-want +got):\n%s", d)
	}
}
