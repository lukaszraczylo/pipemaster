package gha

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// neverCron is a cron expression that names a day no month has, so a schedule written with it never starts a run.
// GitLab keeps the cron outside the CI file and GHA needs one.
const neverCron = "0 0 31 2 *"

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
		for _, ev := range fired {
			switch {
			case ev.Name == "web":
				added = append(added, ir.Trigger{Kind: ir.TriggerManual})
			case ev.Name == "schedule":
				added = append(added, ir.Trigger{Kind: ir.TriggerSchedule})
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

// externalCron writes the schedule of a GitLab source that names no cron: the schedule lives in the project settings,
// and the workflow keeps a cron that never fires until the owner sets the real one.
func (e *emitter) externalCron(path string) string {
	e.external(path, caps.TriggerSchedule, "set the cron expression in on.schedule: GitLab keeps the schedule in the project settings, so the workflow holds "+neverCron+" (never fires)",
		"the schedule names no cron expression")
	e.mark(caps.TriggerSchedule, path)
	return neverCron
}
