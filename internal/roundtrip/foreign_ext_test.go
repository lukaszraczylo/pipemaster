package roundtrip_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// A step marker holds the step so the parser can put it back. It must not hold the Ext of another provider,
// because round-trip metadata keeps that, and a copy in the marker returns it on a file that never had it.
func TestStepMarkerKeepsNoExtOfAnotherProvider(t *testing.T) {
	for _, id := range []string{provider.Argo, provider.Drone, provider.Tekton, provider.Woodpecker} {
		t.Run(id, func(t *testing.T) {
			st := &ir.Step{
				ID: "checkout", Kind: ir.StepAction,
				Action: &ir.ActionRef{Kind: ir.ActionRepo, Host: "https://github.com", Owner: "actions", Repo: "checkout", Ref: "v4"},
				Ext:    ir.Ext{"concourse": map[string]any{"input_mapping": map[string]any{"workspace": "source"}}},
			}
			p := &ir.Project{Origin: ir.Origin{Provider: provider.Concourse, Dialect: provider.Concourse},
				Pipelines: []*ir.Pipeline{{ID: "ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}},
					Jobs: []*ir.Job{{ID: "ci", Steps: []*ir.Step{st, {ID: "build", Kind: ir.StepScript, Script: []string{"make"}}}}}}}}
			pv := prov(t, id)
			files, _, err := pv.Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			back, _, err := pv.Parse(toFS(files))
			if err != nil {
				t.Fatal(err)
			}
			for _, j := range back.Pipelines[0].Jobs {
				for _, s := range j.Steps {
					if _, ok := s.Ext[provider.Concourse]; ok {
						t.Errorf("step %s has the Ext of another provider: %v", s.ID, s.Ext)
					}
				}
			}
		})
	}
}
