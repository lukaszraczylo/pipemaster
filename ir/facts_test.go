package ir_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestRulesOutTags(t *testing.T) {
	tr := func(k ir.TriggerKind) ir.Trigger { return ir.Trigger{Kind: k} }
	for _, c := range []struct {
		name string
		pl   ir.Pipeline
		want bool
	}{
		{"push and pr", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerPush), tr(ir.TriggerPR)}}, true},
		{"schedule", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerSchedule)}}, false},
		{"no trigger", ir.Pipeline{}, false},
		{"tag", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerPush), tr(ir.TriggerTag)}}, false},
		{"manual may run on a tag", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerPush), tr(ir.TriggerManual)}}, false},
		{"api", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerAPI)}}, false},
		{"pipeline done", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerPipelineDone)}}, false},
		{"resource", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerResource)}}, false},
		{"pull_request_target names a branch", ir.Pipeline{Triggers: []ir.Trigger{
			{Kind: ir.TriggerPR, Ext: ir.Ext{"gha": map[string]any{"event": "pull_request_target"}}}}}, false},
		{"kept event", ir.Pipeline{Triggers: []ir.Trigger{tr(ir.TriggerPush)},
			Ext: ir.Ext{"gha": map[string]any{"events": map[string]any{"release": nil}}}}, false},
	} {
		if got := c.pl.RulesOutTags(); got != c.want {
			t.Errorf("%s: RulesOutTags = %v, want %v", c.name, got, c.want)
		}
	}
}
