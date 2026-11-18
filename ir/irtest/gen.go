// Package irtest generates random IR projects for property tests.
package irtest

import (
	"fmt"
	"time"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"pgregory.net/rapid"
)

// Fragment selects the features a generated project may use.
type Fragment func(caps.Feature) bool

const (
	maxJobs  = 3
	textExpr = `[ -~]{1,12}`
)

type gen struct {
	t    *rapid.T
	frag Fragment
	n    int
}

// GenProject draws a valid project that uses only features frag allows, each at least once where an IR field can carry it.
// Dependencies: job.needs.artifacts implies job.needs; job.environment.approval implies job.environment.
// Opaque is the only Expr type, so job.if and step.if are generated only when frag also allows expr.opaque,
// and expr.opaque appears only if a carrier (job.if, step.if, pipeline.env, job.env, step.env, step.action, job.services) is allowed.
// script.interpolation is derived from script text, so it is never generated.
func GenProject(t *rapid.T, frag Fragment) *ir.Project {
	g := &gen{t: t, frag: frag}
	p := &ir.Project{Origin: ir.Origin{Provider: "gha", Dialect: "gha", Files: []string{"a.yml"}}}
	for i, n := 0, rapid.IntRange(1, 2).Draw(t, "pipelines"); i < n; i++ {
		p.Pipelines = append(p.Pipelines, g.pipeline(fmt.Sprintf("p%d", i)))
	}
	return p
}

func (g *gen) label(prefix string) string {
	g.n++
	return fmt.Sprintf("%s%d", prefix, g.n)
}

func (g *gen) str() string { return rapid.StringMatching(textExpr).Draw(g.t, g.label("s")) }
func (g *gen) strs() []string {
	return rapid.SliceOfNDistinct(rapid.StringMatching(textExpr), 1, 3, func(s string) string { return s }).Draw(g.t, g.label("ss"))
}
func (g *gen) flag() bool { return rapid.Bool().Draw(g.t, g.label("b")) }

func (g *gen) expr() expr.Expr {
	return expr.Opaque{Dialect: rapid.SampledFrom([]string{"gha", "gitlab"}).Draw(g.t, g.label("d")), Text: g.str()}
}

func (g *gen) ext() ir.Ext {
	return ir.Ext{"gha": map[string]any{g.str(): g.str(), "n": rapid.IntRange(0, 99).Draw(g.t, g.label("n")), "l": []any{g.str()}}}
}

func (g *gen) value() ir.Value {
	switch rapid.IntRange(0, 2).Draw(g.t, g.label("k")) {
	case 1:
		if g.frag(caps.ExprOpaque) {
			return ir.Value{Kind: ir.ValueExpr, Expr: g.expr()}
		}
		return ir.Value{Lit: g.str()}
	case 2:
		return ir.Value{Kind: ir.ValueSecret, Secret: g.str()}
	}
	return ir.Value{Lit: g.str()}
}

func (g *gen) vars() []ir.Var {
	first := ir.Value{Kind: ir.ValueSecret, Secret: g.str()}
	if g.frag(caps.ExprOpaque) {
		first = ir.Value{Kind: ir.ValueExpr, Expr: g.expr()}
	}
	return []ir.Var{{Name: g.str(), Value: first}, {Name: g.str(), Value: g.value()}}
}

func (g *gen) image() ir.Image {
	return ir.Image{Ref: g.str(), Entrypoint: g.strs()}
}

func (g *gen) duration() *ir.Duration {
	d := ir.Duration(time.Duration(rapid.IntRange(1, 7200).Draw(g.t, g.label("dur"))) * time.Second)
	return &d
}

func (g *gen) pipeline(id string) *ir.Pipeline {
	f := g.frag
	pl := &ir.Pipeline{ID: id, Name: g.str()}
	if f(caps.PipelineEnv) {
		pl.Env = g.vars()
	}
	if f(caps.PipelineInputs) {
		pl.Inputs = []ir.Input{{Name: g.str(), Type: g.str(), Default: g.str(), Description: g.str(), Required: g.flag()}}
	}
	if f(caps.PipelineConcurrency) {
		pl.Concurrency = &ir.Concurrency{Group: g.str(), CancelInProgress: g.flag()}
	}
	if f(caps.PipelinePermissions) {
		pl.Permissions = &ir.Permissions{Scopes: map[string]string{g.str(): g.str()}, OIDC: g.flag()}
	}
	if f(caps.ExtPassthrough) {
		pl.Ext = g.ext()
	}
	pl.Triggers = g.triggers()
	var ids []string
	minJobs := 1
	if f(caps.JobNeeds) || f(caps.JobNeedsArtifacts) {
		minJobs = 2
	}
	for i, n := 0, rapid.IntRange(minJobs, maxJobs).Draw(g.t, g.label("jobs")); i < n; i++ {
		j := g.job(fmt.Sprintf("j%d", i), ids)
		ids = append(ids, j.ID)
		pl.Jobs = append(pl.Jobs, j)
	}
	if f(caps.PipelineFinally) {
		pl.Finally = []*ir.Job{g.job("fin", ids)}
	}
	return pl
}

func (g *gen) filter() ir.Filter {
	return ir.Filter{Include: g.strs(), Exclude: g.strs()}
}

func (g *gen) triggers() []ir.Trigger {
	f := g.frag
	kinds := []struct {
		k  ir.TriggerKind
		ft caps.Feature
	}{
		{ir.TriggerPush, caps.TriggerPush}, {ir.TriggerPR, caps.TriggerPullRequest}, {ir.TriggerTag, caps.TriggerTag},
		{ir.TriggerSchedule, caps.TriggerSchedule}, {ir.TriggerManual, caps.TriggerManual}, {ir.TriggerAPI, caps.TriggerAPI},
		{ir.TriggerPipelineDone, caps.TriggerPipelineDone}, {ir.TriggerResource, caps.TriggerResource},
	}
	var out []ir.Trigger
	filterable := false
	for _, k := range kinds {
		if !f(k.ft) {
			continue
		}
		t := ir.Trigger{Kind: k.k}
		switch k.k {
		case ir.TriggerSchedule:
			t.Cron = "0 3 * * *"
		case ir.TriggerResource:
			t.Resource = &ir.ResourceRef{Name: g.str(), Type: g.str()}
		case ir.TriggerPush, ir.TriggerPR, ir.TriggerTag:
			filterable = true
			g.filters(&t)
		}
		if f(caps.ExtPassthrough) {
			t.Ext = g.ext()
		}
		out = append(out, t)
	}
	if !filterable && (f(caps.TriggerFilterBranch) || f(caps.TriggerFilterTags) || f(caps.TriggerFilterPaths)) {
		t := ir.Trigger{}
		g.filters(&t)
		out = append(out, t)
	}
	return out
}

func (g *gen) filters(t *ir.Trigger) {
	if g.frag(caps.TriggerFilterBranch) {
		t.Branches = g.filter()
	}
	if g.frag(caps.TriggerFilterTags) {
		t.Tags = g.filter()
	}
	if g.frag(caps.TriggerFilterPaths) {
		t.Paths = g.filter()
	}
}

func (g *gen) job(id string, prior []string) *ir.Job {
	f := g.frag
	j := &ir.Job{ID: id, Name: g.str()}
	wantNeeds := f(caps.JobNeeds) || f(caps.JobNeedsArtifacts)
	if wantNeeds && len(prior) > 0 {
		j.Needs = []ir.Need{{JobID: prior[0], Artifacts: f(caps.JobNeedsArtifacts), Optional: g.flag()}}
	}
	if f(caps.JobIf) && f(caps.ExprOpaque) {
		j.If = g.expr()
	}
	if f(caps.JobRunner) {
		j.Runner = ir.Runner{Labels: g.strs(), Platform: g.str(), Selector: map[string]string{g.str(): g.str()}}
	}
	if f(caps.JobContainer) {
		img := g.image()
		j.Container = &img
	}
	if f(caps.JobServices) {
		j.Services = []ir.Service{{Name: g.str(), Image: g.image(), Env: g.vars(), Ports: g.strs()}}
	}
	if f(caps.JobMatrix) {
		ff := g.flag()
		j.Matrix = &ir.Matrix{
			Axes:        []ir.Axis{{Name: "os", Values: g.strs()}, {Name: "v", Values: g.strs()}},
			Include:     []ir.Combo{{{Key: "a", Value: g.str()}, {Key: "b", Value: g.str()}}},
			Exclude:     []ir.Combo{{{Key: "a", Value: g.str()}}},
			MaxParallel: rapid.IntRange(1, 8).Draw(g.t, g.label("mp")),
			FailFast:    &ff,
		}
	}
	if f(caps.JobTimeout) {
		j.Timeout = g.duration()
	}
	if f(caps.JobRetry) {
		j.Retry = &ir.Retry{Attempts: rapid.IntRange(1, 5).Draw(g.t, g.label("ra")), On: g.strs()}
	}
	j.AllowFailure = f(caps.JobAllowFailure)
	if f(caps.JobEnvironment) || f(caps.JobEnvironmentApproval) {
		j.Environment = &ir.Environment{Name: g.str(), URL: g.str(), Approval: f(caps.JobEnvironmentApproval)}
	}
	j.Manual = f(caps.JobManual)
	if f(caps.JobCache) {
		j.Cache = []ir.Cache{{Key: g.str(), Paths: g.strs(), Policy: g.str()}}
	}
	if f(caps.JobArtifacts) {
		j.Artifacts = []ir.Artifact{{Name: g.str(), Paths: g.strs(), Expire: g.str()}}
	}
	if f(caps.JobOutputs) {
		j.Outputs = []ir.Output{{Name: g.str(), Value: g.str()}}
	}
	if f(caps.JobEnv) {
		j.Env = g.vars()
	}
	if f(caps.ExtPassthrough) {
		j.Ext = g.ext()
	}
	for i, n := 0, rapid.IntRange(1, maxJobs).Draw(g.t, g.label("steps")); i < n; i++ {
		j.Steps = append(j.Steps, g.step(fmt.Sprintf("s%d", i)))
	}
	return j
}

func (g *gen) step(id string) *ir.Step {
	f := g.frag
	s := &ir.Step{ID: id, Name: g.str(), Kind: ir.StepScript}
	if f(caps.StepScript) {
		s.Script = g.strs()
	}
	if f(caps.StepAction) {
		s.Kind = ir.StepAction
		s.Action = &ir.ActionRef{Kind: ir.ActionRepo, Owner: g.str(), Repo: g.str(), Path: g.str(), Ref: g.str()}
		w := ir.Value{Lit: g.str()}
		if f(caps.ExprOpaque) {
			w = ir.Value{Kind: ir.ValueExpr, Expr: g.expr()}
		}
		s.With = map[string]ir.Value{g.str(): w}
	}
	if f(caps.StepImage) {
		img := g.image()
		s.Image = &img
	}
	if f(caps.StepIf) && f(caps.ExprOpaque) {
		s.If = g.expr()
	}
	if f(caps.StepEnv) {
		s.Env = g.vars()
	}
	s.ContinueOnError = f(caps.StepContinueOnError)
	if f(caps.StepTimeout) {
		s.Timeout = g.duration()
	}
	if f(caps.StepWorkdir) {
		s.WorkDir = g.str()
	}
	if f(caps.StepShell) {
		s.Shell = g.str()
	}
	if f(caps.ExtPassthrough) {
		s.Ext = g.ext()
	}
	return s
}
