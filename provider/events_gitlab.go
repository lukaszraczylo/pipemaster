package provider

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// gitlabMergeRequest is the pipeline source of a merge request pipeline. GitLab starts one when the merge request is
// created, when its source branch gets a push and on Run pipeline, for a job that has a merge request rule
// (https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/). It is not a default event, so a job with no rules
// is not part of it.
var gitlabMergeRequest = SourceEvent{Name: "merge_request_event", Kind: ir.TriggerPR, Woodpecker: "pull_request", Same: true, Feature: caps.TriggerPullRequest}

// gitlabPRActions are the pull request actions that start a merge request pipeline: the merge request is created
// or its source branch gets a push.
var gitlabPRActions = []string{"opened", "synchronize"}

// prGate keeps a job with no rules out of a merge request pipeline. It is a negated equality, a shape that no GitLab
// rule has, so a restore tells it from a condition of the source.
var prGate expr.Expr = expr.Unary{Op: expr.OpNot, X: expr.Binary{
	Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxEvent}}, R: expr.Lit{Kind: expr.LitStr, Str: gitlabMergeRequest.Name},
}}

// prTarget is true for a target that gains the pull request trigger of a GitLab pipeline with the default triggers,
// and with it the gate on the jobs that have no rules.
func prTarget(target string) bool {
	return Family(target) == GHA || target == Tekton || target == Argo
}

// gitlabHandledByGHA is true for a source whose trigger the GHA emitter makes itself from the default events.
func gitlabHandledByGHA(name string) bool {
	return name == "schedule" || name == "api" || name == "trigger"
}

// IsGitLabDefault is true for a GitLab pipeline that has no workflow block: it runs on every pipeline source.
func IsGitLabDefault(pl *ir.Pipeline) bool {
	return pl != nil && !hasWorkflow(pl) && defaultPushTag(pl.Triggers)
}

// FreshGitLabDefaults names the pipelines that WithDefaultTriggers extends with the manual and pull request triggers
// for target: a GitLab pipeline that the parser gave the default push and tag triggers.
func FreshGitLabDefaults(p *ir.Project, target string) map[string]bool {
	out := map[string]bool{}
	if p == nil || p.Origin.Provider != GitLab || SameFamily(p.Origin.Provider, target) {
		return out
	}
	for _, pl := range p.Pipelines {
		if IsGitLabDefault(pl) {
			out[pl.ID] = true
		}
	}
	return out
}

// IsGitLabInjected is IsGitLabDefault for a pipeline that WithDefaultTriggers has extended with the manual and pull
// request triggers.
func IsGitLabInjected(pl *ir.Pipeline) bool {
	return pl != nil && !hasWorkflow(pl) && injectedPushTag(pl.Triggers)
}

// namesMergeRequest is true when a job or step condition of pl tests for the merge request source.
func namesMergeRequest(pl *ir.Pipeline) bool {
	from := []string{gitlabMergeRequest.Name}
	for _, ev := range gitlabEvents {
		from = append(from, ev.Name)
	}
	named := func(c expr.Expr) bool {
		if c == nil {
			return false
		}
		out := expr.EventsPossible(c, from)
		return len(out) < len(from) && slices.Contains(out, gitlabMergeRequest.Name)
	}
	for _, j := range pl.Jobs {
		if j == nil {
			continue
		}
		if named(j.If) {
			return true
		}
		for _, st := range j.Steps {
			if st != nil && named(st.If) {
				return true
			}
		}
	}
	return false
}

// gitlabDefaultTriggers makes the triggers that a GitLab pipeline with the default triggers also has: the manual start
// (web), and the pull request trigger when a job names the merge request source and the target can gate the jobs that
// have no rules. It returns the pipeline with the gated jobs and the events that have no IR trigger.
func gitlabDefaultTriggers(pl *ir.Pipeline, target string) (*ir.Pipeline, []SourceEvent) {
	cp := *pl
	cp.Triggers = slices.Clone(pl.Triggers)
	var unmapped []SourceEvent
	for _, ev := range firedEvents(pl, gitlabEvents) {
		switch {
		case ev.Kind == ir.TriggerPush || ev.Kind == ir.TriggerTag:
		case ev.Kind != "":
			cp.Triggers = append(cp.Triggers, ir.Trigger{Kind: ev.Kind})
		case Family(target) == GHA && gitlabHandledByGHA(ev.Name):
		default:
			unmapped = append(unmapped, ev)
		}
	}
	named := namesMergeRequest(pl)
	if target == Concourse && named {
		// A Concourse git resource has no pull request version, so the job of that source never starts.
		unmapped = append(unmapped, gitlabMergeRequest)
	}
	if prTarget(target) && named {
		cp.Triggers = append(cp.Triggers, ir.Trigger{Kind: ir.TriggerPR, Ext: ir.Ext{"gha": map[string]any{"types": slices.Clone(gitlabPRActions)}}})
		cp.Jobs = gateJobList(pl.Jobs)
		cp.Finally = gateJobList(pl.Finally)
	}
	ir.NormalizeTriggers(cp.Triggers)
	return &cp, unmapped
}

// gateJobList returns jobs with the gate ANDed into every job that has no rules of the source.
func gateJobList(jobs []*ir.Job) []*ir.Job {
	if jobs == nil {
		return nil
	}
	out := make([]*ir.Job, len(jobs))
	for i, j := range jobs {
		out[i] = j
		if j != nil && ruleless(j.If) {
			c := *j
			c.If = andGate(j.If)
			out[i] = &c
		}
	}
	return out
}

// andGate ANDs the gate into c, which is nil or a status condition.
func andGate(c expr.Expr) expr.Expr {
	if c == nil {
		return prGate
	}
	return expr.Binary{Op: expr.OpAnd, L: c, R: prGate}
}

// ruleless is true for a job condition that a job with no rules has: none, or only job status functions such as
// always() and failure(), which the when key of the job makes.
func ruleless(c expr.Expr) bool {
	switch v := c.(type) {
	case nil:
		return true
	case expr.Call:
		switch v.Name {
		case "always", "failure", "success", "cancelled":
			return len(v.Args) == 0
		}
	case expr.Unary:
		return v.Op == expr.OpNot && ruleless(v.X)
	case expr.Binary:
		return (v.Op == expr.OpAnd || v.Op == expr.OpOr) && ruleless(v.L) && ruleless(v.R)
	}
	return false
}

// GatedJobs lists, as pipeline ID and job ID joined by a slash, the jobs of a GitLab project that an emit to target
// gates for the pull request trigger it adds.
func GatedJobs(p *ir.Project, target string) []string {
	if p == nil || p.Origin.Provider != GitLab || SameFamily(p.Origin.Provider, target) {
		return nil
	}
	var out []string
	for _, pl := range p.Pipelines {
		if !IsGitLabDefault(pl) {
			continue
		}
		cp, _ := gitlabDefaultTriggers(pl, target)
		for _, l := range [][2][]*ir.Job{{pl.Jobs, cp.Jobs}, {pl.Finally, cp.Finally}} {
			for i, j := range l[0] {
				if j != nil && l[1][i] != j {
					out = append(out, pl.ID+"/"+j.ID)
				}
			}
		}
	}
	return out
}

// RestoreGates removes the gate that an emit put on the jobs that gated names, as GatedJobs lists them. The gate
// reads in the dialect of the file, so it is recognised by what it tests, not by its spelling.
func RestoreGates(p *ir.Project, gated []string) {
	for _, pl := range p.Pipelines {
		var ids []string
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			if j != nil && slices.Contains(gated, pl.ID+"/"+j.ID) {
				ids = append(ids, j.ID)
			}
		}
		StripGates(pl, ids)
	}
}

// StripGates removes the gate from the jobs of pl that ids names. A pipeline with a gate also gets the GitLab name of
// the pull request event back in its conditions: a layer that lies between the file and GitLab can hold the name of
// another dialect.
func StripGates(pl *ir.Pipeline, ids []string) {
	if len(ids) == 0 {
		return
	}
	all := append(slices.Clone(pl.Jobs), pl.Finally...)
	for _, j := range all {
		if j != nil && slices.Contains(ids, j.ID) {
			j.If = withoutGate(j.If)
		}
	}
	for _, j := range all {
		if j == nil {
			continue
		}
		j.If = prNameGitLab(j.If)
		for _, st := range j.Steps {
			if st != nil {
				st.If = prNameGitLab(st.If)
			}
		}
	}
}

// InjectedGate is true for the condition that WithDefaultTriggers puts on a job with no rules.
// The gate can sit as a conjunct beside a status condition.
func InjectedGate(c expr.Expr) bool {
	if c == nil {
		return false
	}
	if expr.String(c) == expr.String(prGate) {
		return true
	}
	b, ok := c.(expr.Binary)
	return ok && b.Op == expr.OpAnd && (InjectedGate(b.L) || InjectedGate(b.R))
}

// prNameGitLab spells the pull request event of an event test as GitLab does.
func prNameGitLab(c expr.Expr) expr.Expr {
	switch v := c.(type) {
	case expr.Unary:
		v.X = prNameGitLab(v.X)
		return v
	case expr.Binary:
		if r, ok := v.L.(expr.Ref); ok && len(r.Path) == 1 && r.Path[0] == expr.CtxEvent {
			if l, ok := v.R.(expr.Lit); ok && l.Kind == expr.LitStr && l.Str == "pull_request" && (v.Op == expr.OpEq || v.Op == expr.OpNe) {
				v.R = expr.Lit{Kind: expr.LitStr, Str: gitlabMergeRequest.Name}
			}
			return v
		}
		v.L, v.R = prNameGitLab(v.L), prNameGitLab(v.R)
		return v
	}
	return c
}

// prNameCanon spells the merge request event of an event test as the canonical event, pull_request.
func prNameCanon(c expr.Expr) expr.Expr {
	switch v := c.(type) {
	case expr.Unary:
		v.X = prNameCanon(v.X)
		return v
	case expr.Binary:
		if r, ok := v.L.(expr.Ref); ok && len(r.Path) == 1 && r.Path[0] == expr.CtxEvent {
			if l, ok := v.R.(expr.Lit); ok && l.Kind == expr.LitStr && l.Str == gitlabMergeRequest.Name && (v.Op == expr.OpEq || v.Op == expr.OpNe) {
				v.R = expr.Lit{Kind: expr.LitStr, Str: "pull_request"}
			}
			return v
		}
		v.L, v.R = prNameCanon(v.L), prNameCanon(v.R)
		return v
	}
	return c
}

// CanonEventNames returns p with the merge request event of its conditions spelled as the canonical event, for a
// target that compares the event of its run with exact strings and has no event name of the source.
func CanonEventNames(p *ir.Project) *ir.Project {
	if p == nil || p.Origin.Provider != GitLab {
		return p
	}
	out := ir.Clone(p)
	for _, pl := range out.Pipelines {
		if pl == nil {
			continue
		}
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			if j == nil {
				continue
			}
			j.If = prNameCanon(j.If)
			for _, st := range j.Steps {
				if st != nil {
					st.If = prNameCanon(st.If)
				}
			}
		}
	}
	return out
}

// withoutGate removes the gate from the conjunction c: the gate can sit beside conjuncts that an emitter added.
func withoutGate(c expr.Expr) expr.Expr {
	if isGate(c) {
		return nil
	}
	b, ok := c.(expr.Binary)
	if !ok || b.Op != expr.OpAnd {
		return c
	}
	l, r := withoutGate(b.L), withoutGate(b.R)
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	return expr.Binary{Op: expr.OpAnd, L: l, R: r}
}

// isGate is true for a test that the run is not a pull request, in any spelling of the event.
func isGate(c expr.Expr) bool {
	isPR := func(b expr.Binary) bool {
		l, lok := b.L.(expr.Ref)
		r, rok := b.R.(expr.Lit)
		return lok && rok && len(l.Path) == 1 && l.Path[0] == expr.CtxEvent && r.Kind == expr.LitStr &&
			(strings.Contains(r.Str, "pull_request") || strings.Contains(r.Str, gitlabMergeRequest.Name))
	}
	switch v := c.(type) {
	case expr.Unary:
		if b, ok := v.X.(expr.Binary); ok && v.Op == expr.OpNot && (b.Op == expr.OpEq || b.Op == expr.OpMatch) {
			return isPR(b)
		}
	case expr.Binary:
		if v.Op == expr.OpNe || v.Op == expr.OpNoMatch {
			return isPR(v)
		}
	}
	return false
}
