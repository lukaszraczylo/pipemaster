package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// A pipeline with no job still gets a placeholder job. Its tag trigger cannot start that job together with the push
// trigger, so the report must say that tag runs are lost.
func TestPlaceholderOnlyPipelineReportsTheLostTagTrigger(t *testing.T) {
	p := project()
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main"), {Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}}}
	_, rep := emit(t, p)
	got := entriesAt(rep, caps.Lost, "no job copy can wait for a tag version")
	if len(got) != 1 || got[0].Feature != caps.TriggerTag {
		t.Errorf("want one lost trigger.tag entry, got %+v", rep.Entries)
	}
}

// A script line with an input that Concourse cannot spell is replaced by a failing line. The task must fail, the
// lost entry must exist, and the carry note must not say that the task runs the script.
func TestLostScriptLineFailsLoudAndCarryNoteIsHonest(t *testing.T) {
	p := project(scriptJob("deploy", "oc patch ${{ inputs.name }}", "echo done", ""))
	files, rep := emit(t, p)
	if !strings.Contains(files[".concourse/ci.yml"], "exits 1") && !strings.Contains(files[".concourse/ci.yml"], "exit 1") {
		t.Fatalf("the task has no failing line:\n%s", files[".concourse/ci.yml"])
	}
	if !hasFeatureEntry(rep, string(caps.ScriptInterpolation), "no Concourse spelling") {
		t.Errorf("want a script.interpolation lost entry, got %+v", rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.Feature == caps.StepScript && strings.Contains(e.Detail, "runs the script from its args") {
			t.Errorf("the note claims the script runs: %+v", e)
		}
	}
	if !hasFeatureEntry(rep, string(caps.StepScript), "failing line") {
		t.Errorf("want a step.script note about the failing line, got %+v", rep.Entries)
	}
}
