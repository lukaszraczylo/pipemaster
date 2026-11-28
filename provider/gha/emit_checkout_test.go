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
	p.Origin = ir.Origin{Provider: "argo", Dialect: "argo"}
	if doc := workflowText(t, p); strings.Contains(doc, "actions/checkout") {
		t.Errorf("only a source that clones asks for the checkout:\n%s", doc)
	}
}

func originJob(origin string, ext ir.Ext, env ...ir.Var) *ir.Project {
	p := concourseJob(ext, scriptStep())
	p.Origin = ir.Origin{Provider: origin, Dialect: origin}
	p.Pipelines[0].Env = env
	return p
}

func TestSourceCloneBecomesACheckoutOnGHA(t *testing.T) {
	lit := func(n, v string) ir.Var { return ir.Var{Name: n, Value: ir.Value{Lit: v}} }
	drone := func(m map[string]any) ir.Ext { return ir.Ext{"drone": m} }
	wp := func(m map[string]any) ir.Ext { return ir.Ext{"woodpecker": m} }
	tests := []struct {
		p    *ir.Project
		name string
		want []string
		deny []string
	}{
		{originJob("drone", nil), "drone default clone", []string{"actions/checkout@"}, []string{"fetch-depth"}},
		{originJob("drone", drone(map[string]any{"clone": map[string]any{"depth": 1}})), "drone clone.depth", []string{"actions/checkout@", "fetch-depth: 1"}, nil},
		{originJob("drone", drone(map[string]any{"clone": map[string]any{"disable": true}})), "drone clone.disable", nil, []string{"actions/checkout"}},
		{originJob("drone", drone(map[string]any{"clone": map[string]any{"disable": false, "retries": 3}})), "drone clone.retries only", []string{"actions/checkout@"}, nil},
		{originJob("woodpecker", nil), "woodpecker default clone", []string{"actions/checkout@"}, nil},
		{originJob("woodpecker", wp(map[string]any{"skip_clone": true})), "woodpecker skip_clone", nil, []string{"actions/checkout"}},
		{originJob("woodpecker", wp(map[string]any{"clone": map[string]any{"git": map[string]any{"image": "woodpeckerci/plugin-git", "settings": map[string]any{"depth": 50, "recursive": true}}}})), "woodpecker git settings", []string{"actions/checkout@", "fetch-depth: 50", "submodules: true"}, nil},
		{originJob("woodpecker", wp(map[string]any{"clone": map[string]any{"git": map[string]any{"image": "octocat/custom-git-plugin"}}})), "woodpecker custom clone plugin", nil, []string{"actions/checkout"}},
		{originJob("woodpecker", wp(map[string]any{"clone": map[string]any{"other": map[string]any{}}})), "woodpecker clone section without git", nil, []string{"actions/checkout"}},
		{originJob("gitlab", nil, lit("GIT_DEPTH", "3"), lit("GIT_SUBMODULE_STRATEGY", "recursive")), "gitlab depth and submodules", []string{"actions/checkout@", "fetch-depth: 3", "submodules: recursive"}, nil},
		{originJob("gitlab", nil, lit("GIT_STRATEGY", "none")), "gitlab GIT_STRATEGY none", nil, []string{"actions/checkout"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := workflowText(t, tc.p)
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
			if len(tc.want) > 0 && !strings.Contains(doc, `"pre":1`) {
				t.Errorf("the checkout must count as one pre step:\n%s", doc)
			}
		})
	}
}
