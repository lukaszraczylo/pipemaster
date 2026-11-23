package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestStepMarkerKeepsNoForeignExt(t *testing.T) {
	st := &ir.Step{ID: "s", Kind: ir.StepScript, Script: []string{"echo hi"}, If: expr.Opaque{Dialect: "gha", Text: "env[github.job]"},
		Ext: ir.Ext{"concourse": map[string]any{"input_mapping": map[string]any{"workspace": "source"}}}}
	p := &ir.Project{Origin: ir.Origin{Provider: "concourse", Dialect: "concourse"},
		Pipelines: []*ir.Pipeline{{ID: "app", Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{st}}}}}}
	files, _ := emit(t, p)
	back, _ := parseFiles(t, map[string]string{"workflows/app.yaml": files[".argo/workflows/app.yaml"]})
	for _, s := range back.Pipelines[0].Jobs[0].Steps {
		if _, ok := s.Ext["concourse"]; ok {
			t.Fatalf("step %s kept Ext of another provider: %v", s.ID, s.Ext)
		}
	}
}
