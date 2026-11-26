package gha

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// Pipeline sources that start a run through an API call. GHA has the one event repository_dispatch, and the
// event_type of the call tells the two apart.
var apiSources = []string{"api", "trigger"}

// sourceEventsArg marks the triggers the emitter added for the pipeline sources that the jobs of a GitLab pipeline
// name, so reading the file back drops them.
type sourceEventsArg struct {
	Kinds []ir.TriggerKind `json:"source_events"`
}

// withGitLabSources gives a GitLab pipeline with the default triggers the triggers of the other sources that its job
// conditions admit: web, schedule, api and trigger. The default of GitLab runs a job on each of them, and a workflow
// that names push only never starts the jobs that test for one.
func withGitLabSources(p *ir.Project, target string) (*ir.Project, map[string]sourceEventsArg) {
	if p == nil || p.Origin.Provider != provider.GitLab || provider.SameFamily(p.Origin.Provider, target) {
		return p, nil
	}
	var out *ir.Project
	args := map[string]sourceEventsArg{}
	for i, pl := range p.Pipelines {
		fired := provider.GitLabNamedSources(pl)
		var added []ir.Trigger
		var types []any
		manual := slices.ContainsFunc(pl.Triggers, func(t ir.Trigger) bool { return t.Kind == ir.TriggerManual })
		for _, ev := range fired {
			switch {
			case ev.Name == "web" || ev.Name == "schedule" && !slices.ContainsFunc(pl.Triggers, func(t ir.Trigger) bool { return t.Kind == ir.TriggerSchedule }):
				if !manual {
					manual = true
					added = append(added, ir.Trigger{Kind: ir.TriggerManual})
				}
				if ev.Name == "schedule" {
					added = append(added, ir.Trigger{Kind: ir.TriggerSchedule})
				}
			case slices.Contains(apiSources, ev.Name):
				types = append(types, ev.Name)
			}
		}
		if len(types) > 0 {
			added = append(added, ir.Trigger{Kind: ir.TriggerAPI, Ext: ir.Ext{provider.Family(target): map[string]any{"types": types}}})
		}
		if len(added) == 0 {
			continue
		}
		if out == nil {
			c := *p
			c.Pipelines = slices.Clone(p.Pipelines)
			out = &c
		}
		cp := *pl
		cp.Triggers = append(slices.Clone(pl.Triggers), added...)
		out.Pipelines[i] = &cp
		arg := sourceEventsArg{}
		for _, t := range added {
			arg.Kinds = append(arg.Kinds, t.Kind)
		}
		args[pl.ID] = arg
	}
	if out == nil {
		return p, nil
	}
	return out, args
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
