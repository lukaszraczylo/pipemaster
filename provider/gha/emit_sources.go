package gha

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// Pipeline sources that start a run through an API call. GHA has the one event repository_dispatch, and the
// event_type of the call tells the two apart.
var apiSources = []string{"api", "trigger"}

// sourceEventsArg marks the triggers the emitter added for the pipeline sources that the jobs of a GitLab pipeline
// name, so reading the file back drops them.
type sourceEventsArg struct {
	// Conds holds, by job ID, the condition that withGitLabSources gated with the workflow rules and what it became.
	Conds map[string]condBack `json:"source_conds,omitempty"`
	Kinds []ir.TriggerKind    `json:"source_events"`
	// Gated lists the jobs that got the gate of the pull request trigger, which the parser removes with it.
	Gated []string `json:"gated,omitempty"`
}

// condBack is the job condition of the source (If, empty for none) and the gated condition that replaced it (Back).
type condBack struct {
	If   string `json:"if"`
	Back string `json:"back"`
}

// withGitLabSources gives a GitLab pipeline the triggers of the other sources that its job conditions name: web, schedule,
// api and trigger. The default of GitLab runs a job on each of them, and a workflow that names push only never starts
// the jobs that test for one. The workflow rules decide: a source they exclude gets no trigger. A web or schedule source
// that they admit on some branch or tag gets its trigger, and every job gets the rule condition for a run of that source,
// so no job runs where GitLab starts none. An api or trigger source under such a condition comes back as lost, because a
// repository_dispatch run has no ref of its own to test.
func withGitLabSources(p *ir.Project, target string, injected map[string]bool) (*ir.Project, map[string]sourceEventsArg, map[string][]provider.SourceEvent) {
	if p == nil || p.Origin.Provider != provider.GitLab || provider.SameFamily(p.Origin.Provider, target) {
		return p, nil, nil
	}
	var out *ir.Project
	args := map[string]sourceEventsArg{}
	lost := map[string][]provider.SourceEvent{}
	for i, pl := range p.Pipelines {
		var added []ir.Trigger
		var types []any
		var gates []sourceGate
		has := func(k ir.TriggerKind) bool {
			return slices.ContainsFunc(pl.Triggers, func(t ir.Trigger) bool { return t.Kind == k }) ||
				slices.ContainsFunc(added, func(t ir.Trigger) bool { return t.Kind == k })
		}
		for _, ev := range provider.GitLabNamedSources(pl) {
			if ev.Name != "web" && ev.Name != "schedule" && !slices.Contains(apiSources, ev.Name) {
				continue
			}
			admit, residual := provider.WorkflowAdmits(pl, ev.Name)
			switch {
			case admit == provider.AdmitNo:
				continue
			case admit == provider.AdmitMaybe && residual != nil && !slices.Contains(apiSources, ev.Name):
				// With the default triggers the parser has ANDed the workflow condition into every job already.
				if provider.DefaultPushTag(pl) {
					break
				}
				g := sourceGate{source: ev.Name, cond: residual}
				if !gateTranslates(g) {
					lost[pl.ID] = append(lost[pl.ID], ev)
					continue
				}
				gates = append(gates, g)
			case admit == provider.AdmitMaybe:
				lost[pl.ID] = append(lost[pl.ID], ev)
				continue
			}
			switch {
			case ev.Name == "web" && !has(ir.TriggerManual):
				added = append(added, ir.Trigger{Kind: ir.TriggerManual})
			case ev.Name == "schedule":
				if !has(ir.TriggerManual) {
					added = append(added, ir.Trigger{Kind: ir.TriggerManual})
				}
				if !has(ir.TriggerSchedule) {
					added = append(added, ir.Trigger{Kind: ir.TriggerSchedule})
				}
			case slices.Contains(apiSources, ev.Name) && !has(ir.TriggerAPI):
				types = append(types, ev.Name)
			}
		}
		if len(types) > 0 {
			added = append(added, ir.Trigger{Kind: ir.TriggerAPI, Ext: ir.Ext{provider.Family(target): map[string]any{"types": types}}})
		}
		kinds := injectedKinds(pl, injected)
		if len(added) == 0 {
			if len(kinds) > 0 {
				args[pl.ID] = sourceEventsArg{Kinds: kinds, Gated: gatedIDs(pl)}
			}
			continue
		}
		if out == nil {
			c := *p
			c.Pipelines = slices.Clone(p.Pipelines)
			out = &c
		}
		cp := *pl
		cp.Triggers = append(slices.Clone(pl.Triggers), added...)
		arg := sourceEventsArg{}
		arg.Conds = gateJobs(&cp, gates)
		out.Pipelines[i] = &cp
		arg.Kinds = append(arg.Kinds, kinds...)
		arg.Gated = gatedIDs(&cp)
		for _, t := range added {
			arg.Kinds = append(arg.Kinds, t.Kind)
		}
		args[pl.ID] = arg
	}
	if out == nil {
		return p, args, lost
	}
	return out, args, lost
}

// sourceGate is the condition under which the workflow rules start a pipeline from a source.
type sourceGate struct {
	cond   expr.Expr
	source string
}

// expr is true for a run of another source, and for a run of the source when the rule condition holds.
func (g sourceGate) expr() expr.Expr {
	isSource := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxEvent}}, R: expr.Lit{Kind: expr.LitStr, Str: g.source}}
	return expr.Binary{Op: expr.OpOr, L: expr.Unary{Op: expr.OpNot, X: isSource}, R: g.cond}
}

// gateTranslates is true when the gate reads in GHA. A rule that tests the truthiness of a variable has no GHA form, and
// a gate that cannot be written would disable every job, so the source stays lost.
func gateTranslates(g sourceGate) bool {
	from := expr.SourceFor(provider.GitLab, exprgha.Lang)
	if from == nil {
		return true
	}
	x := g.expr()
	if _, ok := expr.TranslateFacts(x, *from, exprgha.Lang, expr.Facts{}); ok {
		return true
	}
	_, _, ok := expr.TranslateApprox(x, *from, exprgha.Lang)
	return ok
}

// gateJobs ANDs into the condition of every job, for each gate, that a run of the source passes the workflow rules:
// the run is of another source, or the rule condition holds. cp holds the jobs afterwards; the result maps each changed
// job ID to what it was.
func gateJobs(cp *ir.Pipeline, gates []sourceGate) map[string]condBack {
	if len(gates) == 0 {
		return nil
	}
	var gate expr.Expr
	for _, g := range gates {
		one := g.expr()
		if gate == nil {
			gate = one
			continue
		}
		gate = expr.Binary{Op: expr.OpAnd, L: gate, R: one}
	}
	conds := map[string]condBack{}
	jobs := make([]*ir.Job, len(cp.Jobs))
	for i, j := range cp.Jobs {
		c := *j
		was := ""
		if j.If == nil {
			c.If = gate
		} else {
			was = expr.String(j.If)
			c.If = expr.Binary{Op: expr.OpAnd, L: j.If, R: gate}
		}
		conds[j.ID] = condBack{If: was, Back: expr.String(c.If)}
		jobs[i] = &c
	}
	cp.Jobs = jobs
	return conds
}

// restoreGatedConds gives back the job conditions that gateJobs changed, for each job whose condition is still as written.
func restoreGatedConds(pl *ir.Pipeline, arg sourceEventsArg) {
	for _, j := range pl.Jobs {
		w, ok := arg.Conds[j.ID]
		if !ok || j.If == nil || expr.String(j.If) != w.Back {
			continue
		}
		j.If = nil
		if w.If == "" {
			continue
		}
		if orig, err := expr.ParseCanonical(w.If); err == nil {
			j.If = orig
		}
	}
}

// dropSourceEvents removes the triggers that withGitLabSources added.
func dropSourceEvents(pl *ir.Pipeline, arg sourceEventsArg) {
	pl.Triggers = slices.DeleteFunc(pl.Triggers, func(t ir.Trigger) bool { return slices.Contains(arg.Kinds, t.Kind) })
}

// externalSchedule reports a GitLab schedule that names no cron: GitLab keeps the cron in the project settings, and an
// invented time would run the workflow when no schedule asked for it. The workflow has no schedule event, and
// workflow_dispatch keeps the job reachable.
func (e *emitter) externalSchedule(path string) {
	e.external(path, caps.TriggerSchedule, "add on: schedule: - cron: <the cron of your GitLab pipeline schedule> to the workflow: GitLab keeps the schedule in the project settings, so the workflow has no schedule event and the job starts by workflow_dispatch",
		"the schedule names no cron expression")
}

// loseSources reports the sources that the workflow rules admit on some branch or tag only. The job condition names the
// source and the workflow has no event for it, so the job does not start.
func (e *emitter) loseSources(pl *ir.Pipeline) {
	for _, ev := range e.srcLost[pl.ID] {
		e.lose("pipeline/"+pl.ID, ev.Feature, "the workflow rules admit the "+ev.Name+" source only on some branches, tags or variables, and "+
			e.d.title()+" has no ref filter for it; the jobs for that source do not start")
	}
}

// injectedKinds lists the triggers that provider.WithDefaultTriggers added to a GitLab pipeline with the default
// triggers, which the parser drops again: the GitLab parser gives such a pipeline push and tag only.
func injectedKinds(pl *ir.Pipeline, injected map[string]bool) []ir.TriggerKind {
	if !injected[pl.ID] || !provider.IsGitLabInjected(pl) {
		return nil
	}
	var out []ir.TriggerKind
	for _, t := range pl.Triggers {
		if t.Kind == ir.TriggerManual || t.Kind == ir.TriggerPR {
			out = append(out, t.Kind)
		}
	}
	return out
}

// gatedIDs lists the jobs of pl that carry the gate of the pull request trigger.
func gatedIDs(pl *ir.Pipeline) []string {
	var out []string
	for _, j := range pl.Jobs {
		if j != nil && provider.InjectedGate(j.If) {
			out = append(out, j.ID)
		}
	}
	return out
}
