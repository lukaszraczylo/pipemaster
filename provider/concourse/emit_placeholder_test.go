package concourse_test

import (
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
