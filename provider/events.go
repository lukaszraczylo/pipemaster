package provider

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// SourceEvent is an event on which a pipeline runs when its file names no trigger.
type SourceEvent struct {
	Name       string
	Kind       ir.TriggerKind
	Woodpecker string
	Feature    caps.Feature
	Same       bool
}

// Drone runs a pipeline with no trigger on each of its events: cron, custom, push, pull_request, tag, promote and
// rollback (https://docs.drone.io/pipeline/triggers/). Woodpecker names push, pull_request, tag, cron, manual and
// deployment (https://woodpecker-ci.org/docs/usage/workflow-syntax#event); a Drone promotion and a rollback are
// the nearest of a deployment.
var droneEvents = []SourceEvent{
	{Name: "push", Kind: ir.TriggerPush, Woodpecker: "push", Same: true, Feature: caps.TriggerPush},
	{Name: "pull_request", Kind: ir.TriggerPR, Woodpecker: "pull_request", Same: true, Feature: caps.TriggerPullRequest},
	{Name: "tag", Kind: ir.TriggerTag, Woodpecker: "tag", Same: true, Feature: caps.TriggerTag},
	{Name: "cron", Woodpecker: "cron", Same: true, Feature: caps.TriggerSchedule},
	{Name: "custom", Kind: ir.TriggerManual, Woodpecker: "manual", Same: true, Feature: caps.TriggerManual},
	{Name: "promote", Woodpecker: "deployment", Feature: caps.TriggerAPI},
	{Name: "rollback", Woodpecker: "deployment", Feature: caps.TriggerAPI},
}

// GitLab runs a pipeline with no workflow:rules for every pipeline source
// (https://docs.gitlab.com/ci/jobs/job_rules/#ci_pipeline_source-predefined-variable). A merge request pipeline
// exists only for a job with a merge request rule (https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/),
// so the default has none.
var gitlabEvents = []SourceEvent{
	{Name: "push", Kind: ir.TriggerPush, Woodpecker: "push", Same: true, Feature: caps.TriggerPush},
	{Name: "tag", Kind: ir.TriggerTag, Woodpecker: "tag", Same: true, Feature: caps.TriggerTag},
	{Name: "web", Kind: ir.TriggerManual, Woodpecker: "manual", Same: true, Feature: caps.TriggerManual},
	{Name: "schedule", Woodpecker: "cron", Same: true, Feature: caps.TriggerSchedule},
	{Name: "api", Woodpecker: "manual", Feature: caps.TriggerAPI},
	{Name: "trigger", Woodpecker: "manual", Feature: caps.TriggerAPI},
	{Name: "pipeline", Feature: caps.TriggerPipelineDone},
	{Name: "parent_pipeline", Feature: caps.TriggerPipelineDone},
	{Name: "external", Feature: caps.TriggerResource},
	{Name: "chat", Feature: caps.TriggerResource},
}

// DefaultEvents lists the events on which a pipeline of the origin runs when it names no trigger. It is nil when the
// pipeline names one, when the origin starts nothing by itself, or when a GitLab file has a workflow block, which
// then decides.
func DefaultEvents(pl *ir.Pipeline, origin string) []SourceEvent {
	if pl == nil || len(pl.Triggers) > 0 {
		return nil
	}
	switch origin {
	case Drone:
		return firedEvents(pl, droneEvents)
	case GitLab:
		if m, _ := pl.Ext[GitLab].(map[string]any); m != nil {
			if _, has := m["workflow"]; has {
				return nil
			}
		}
		return firedEvents(pl, gitlabEvents)
	}
	return nil
}

// firedEvents keeps the events on which a job can run: the condition of a job and of its steps can rule an event out,
// as an event filter of the source with no positive list does. A condition that names no event keeps every event.
func firedEvents(pl *ir.Pipeline, events []SourceEvent) []SourceEvent {
	names := make([]string, len(events))
	for i, ev := range events {
		names[i] = ev.Name
	}
	possible := func(c expr.Expr, from []string) []string {
		if c == nil {
			return from
		}
		out := expr.EventsPossible(c, from)
		// A tag run is a push of a tag, so the condition must admit that run, not only the name tag.
		if i := slices.Index(out, "tag"); i >= 0 && len(expr.EventsPossible(c, []string{expr.TagPush})) == 0 {
			out = slices.Delete(slices.Clone(out), i, i+1)
		}
		return out
	}
	var allowed []string
	for _, j := range pl.Jobs {
		if j == nil {
			continue
		}
		jobEvents := possible(j.If, names)
		var stepEvents []string
		for _, st := range j.Steps {
			if st != nil {
				stepEvents = append(stepEvents, possible(st.If, jobEvents)...)
			}
		}
		if len(j.Steps) == 0 {
			stepEvents = jobEvents
		}
		allowed = append(allowed, stepEvents...)
	}
	if len(pl.Jobs) == 0 {
		allowed = names
	}
	return slices.DeleteFunc(slices.Clone(events), func(ev SourceEvent) bool { return !slices.Contains(allowed, ev.Name) })
}

// DefaultTriggersArg is the marker argument that tells a parser the triggers of the pipeline were synthesised from
// the default events of its source, so reading the file back restores a pipeline with no trigger.
type DefaultTriggersArg struct {
	Default bool `json:"default_triggers"`
}

// WithDefaultTriggers gives each pipeline with no trigger the IR triggers of its source's default events, unless the
// target is the source's own family, which reads the file back as it was. The
// second result lists, per pipeline ID, the events that have no IR trigger, and the third names the pipelines that got triggers.
func WithDefaultTriggers(p *ir.Project, target string) (*ir.Project, map[string][]SourceEvent, map[string]bool) {
	if p == nil {
		return p, nil, nil
	}
	var out *ir.Project
	unmapped := map[string][]SourceEvent{}
	injected := map[string]bool{}
	for i, pl := range p.Pipelines {
		events := DefaultEvents(pl, p.Origin.Provider)
		if len(events) == 0 || SameFamily(p.Origin.Provider, target) {
			continue
		}
		if out == nil {
			c := *p
			c.Pipelines = append([]*ir.Pipeline(nil), p.Pipelines...)
			out = &c
		}
		cp := *pl
		injected[pl.ID] = true
		for _, ev := range events {
			if ev.Kind == "" {
				unmapped[pl.ID] = append(unmapped[pl.ID], ev)
				continue
			}
			cp.Triggers = append(cp.Triggers, ir.Trigger{Kind: ev.Kind})
		}
		out.Pipelines[i] = &cp
	}
	if out == nil {
		return p, nil, nil
	}
	return out, unmapped, injected
}

// NoFormDetail words the report entry for source default events that the target has no trigger form for.
func NoFormDetail(names []string, title string) string {
	return "the source runs on the events " + strings.Join(names, ", ") + " and its file has no trigger form for them; " + title + " has no event for them, and the file names no schedule to copy"
}

// EventGroup lists the source events that one trigger feature reports.
type EventGroup struct {
	Feature caps.Feature
	Names   []string
}

// GroupByFeature groups events by the feature that reports them, in first-seen order, so that one report entry names
// every event of a feature.
func GroupByFeature(events []SourceEvent) []EventGroup {
	var out []EventGroup
	for _, ev := range events {
		found := false
		for i := range out {
			if out[i].Feature == ev.Feature {
				out[i].Names = append(out[i].Names, ev.Name)
				found = true
			}
		}
		if !found {
			out = append(out, EventGroup{Feature: ev.Feature, Names: []string{ev.Name}})
		}
	}
	return out
}

// WoodpeckerEvents maps the default events of origin to Woodpecker events. approx lists the source events whose
// Woodpecker event only has the same effect, and lost lists those with no Woodpecker event.
func WoodpeckerEvents(origin string) (events []string, approx, lost []SourceEvent) {
	var src []SourceEvent
	switch origin {
	case Drone:
		src = droneEvents
	case GitLab:
		src = gitlabEvents
	default:
		src = []SourceEvent{droneEvents[0], droneEvents[1], droneEvents[2]}
	}
	for _, ev := range src {
		switch {
		case ev.Woodpecker == "":
			lost = append(lost, ev)
			continue
		case !ev.Same:
			approx = append(approx, ev)
		}
		if !slices.Contains(events, ev.Woodpecker) {
			events = append(events, ev.Woodpecker)
		}
	}
	return events, approx, lost
}
