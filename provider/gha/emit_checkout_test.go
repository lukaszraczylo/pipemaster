package gha_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

func concourseJob(ext ir.Ext, steps ...*ir.Step) *ir.Project {
	pl := &ir.Pipeline{ID: "ci", Jobs: []*ir.Job{{ID: "build", Ext: ext, Steps: steps}}}
	return &ir.Project{Origin: ir.Origin{Provider: "concourse", Dialect: "concourse"}, Pipelines: []*ir.Pipeline{pl}}
}

func scriptStep() *ir.Step {
	return &ir.Step{ID: "s", Kind: ir.StepScript, Script: []string{"make"}}
}

func workflowText(t *testing.T, p *ir.Project) string {
	t.Helper()
	_, files := emitFS(t, p)
	var doc string
	for _, f := range files {
		doc = string(f)
	}
	return doc
}

func TestConcourseSourceJobGetsACheckout(t *testing.T) {
	tests := []struct {
		name string
		ext  ir.Ext
		want []string
		deny []string
	}{
		{"repository get", ir.Ext{"concourse": map[string]any{emitkit.CheckoutKey: map[string]any{}}}, []string{"actions/checkout@"}, []string{"fetch-depth"}},
		{"get with depth", ir.Ext{"concourse": map[string]any{emitkit.CheckoutKey: map[string]any{"depth": 1}}}, []string{"actions/checkout@", "fetch-depth: 1"}, nil},
		{"no repository get", ir.Ext{"concourse": map[string]any{"gets": []any{}}}, nil, []string{"actions/checkout"}},
		{"no Ext", nil, nil, []string{"actions/checkout"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := workflowText(t, concourseJob(tc.ext, scriptStep()))
			for _, w := range tc.want {
				if !strings.Contains(doc, w) {
					t.Errorf("missing %q:\n%s", w, doc)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(doc, d) {
					t.Errorf("unexpected %q:\n%s", d, doc)
				}
			}
		})
	}
}

func TestConcourseSourceJobWithoutStepsGetsNoCheckout(t *testing.T) {
	ext := ir.Ext{"concourse": map[string]any{emitkit.CheckoutKey: map[string]any{}}}
	if doc := workflowText(t, concourseJob(ext)); strings.Contains(doc, "actions/checkout") {
		t.Errorf("a job with no steps needs no checkout:\n%s", doc)
	}
}

func TestOtherSourceIgnoresTheCheckoutKey(t *testing.T) {
	p := concourseJob(ir.Ext{"concourse": map[string]any{emitkit.CheckoutKey: map[string]any{}}}, scriptStep())
	p.Origin = ir.Origin{Provider: "drone", Dialect: "drone"}
	if doc := workflowText(t, p); strings.Contains(doc, "actions/checkout") {
		t.Errorf("only a Concourse source asks for the checkout:\n%s", doc)
	}
}
