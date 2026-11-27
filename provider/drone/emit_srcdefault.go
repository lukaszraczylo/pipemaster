package drone

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// scheduleEvent is the default event of a GitLab pipeline that Drone holds as the cron event.
const scheduleEvent = "schedule"

// withSourceDefaults gives a GitLab pipeline with the default triggers the triggers of its other sources. The
// schedule becomes a cron trigger with no cron name: Drone starts it only when a person adds a cron job.
func withSourceDefaults(p *ir.Project) (*ir.Project, map[string][]provider.SourceEvent, map[string]bool) {
	p, noForm, injected := provider.WithDefaultTriggers(p, dialect)
	for _, pl := range p.Pipelines {
		if pl == nil || !injected[pl.ID] {
			continue
		}
		events := noForm[pl.ID]
		i := slices.IndexFunc(events, func(ev provider.SourceEvent) bool { return ev.Name == scheduleEvent })
		if i < 0 {
			continue
		}
		noForm[pl.ID] = slices.Delete(slices.Clone(events), i, i+1)
		pl.Triggers = append(slices.Clone(pl.Triggers), ir.Trigger{Kind: ir.TriggerSchedule})
		ir.NormalizeTriggers(pl.Triggers)
	}
	return p, noForm, injected
}

// noteDefaultEvents reports the default events of the source that have no IR trigger.
func (e *emitter) noteDefaultEvents(noForm map[string][]provider.SourceEvent) {
	for id, events := range noForm {
		for _, g := range provider.GroupByFeature(events) {
			e.lose("pipeline/"+id, g.Feature, provider.NoFormDetail(g.Names, "Drone"))
		}
	}
}

// defaultTriggers is true when a document of the pipeline marks its triggers as made from the default events of the
// source.
func (g *group) defaultTriggers() bool {
	return slices.ContainsFunc(g.docs, func(d *doc) bool {
		u, ok := d.marks.markedWith(caps.TriggerPush, pipelinePth)
		var a provider.DefaultTriggersArg
		return ok && decodeArg(d.marks.args[u], &a) && a.Default
	})
}

// markDefaultTriggers writes the marker that tells the parser the triggers were made from the default events.
func (e *emitter) markDefaultTriggers(pl *ir.Pipeline) error {
	if !e.injected[pl.ID] {
		return nil
	}
	return e.markArg(caps.TriggerPush, "pipeline/"+pl.ID, provider.DefaultMarker(pl))
}
