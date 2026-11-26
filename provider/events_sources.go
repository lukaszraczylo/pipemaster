package provider

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// GitLabNamedSources lists the pipeline sources that a job or step condition of a GitLab pipeline names, when the
// pipeline has the default push and tag triggers or a workflow block, which WorkflowAdmits reads. A condition that
// tests no source names none, so a job with no source test adds no event; a test such as `source != push` names every
// other source.
func GitLabNamedSources(pl *ir.Pipeline) []SourceEvent {
	if pl == nil || !hasWorkflow(pl) && !defaultPushTag(pl.Triggers) {
		return nil
	}
	names := make([]string, len(gitlabEvents))
	for i, ev := range gitlabEvents {
		names[i] = ev.Name
	}
	var named []string
	collect := func(c expr.Expr, from []string) []string {
		if c == nil {
			return from
		}
		out := expr.EventsPossible(c, from)
		if len(out) < len(from) {
			named = append(named, out...)
		}
		return out
	}
	for _, j := range pl.Jobs {
		if j == nil {
			continue
		}
		jobEvents := collect(j.If, names)
		for _, st := range j.Steps {
			if st != nil {
				collect(st.If, jobEvents)
			}
		}
	}
	return slices.DeleteFunc(slices.Clone(gitlabEvents), func(ev SourceEvent) bool { return !slices.Contains(named, ev.Name) })
}

// DefaultPushTag is true for the two bare triggers that the GitLab parser gives a pipeline with no workflow rules, or
// with rules that have no trigger form. The parser then ANDs the workflow condition into every job.
func DefaultPushTag(pl *ir.Pipeline) bool { return defaultPushTag(pl.Triggers) }

// defaultPushTag is true for the two bare triggers that the GitLab parser gives a pipeline with no workflow rules.
func defaultPushTag(ts []ir.Trigger) bool {
	return len(ts) == 2 && ts[0].Kind == ir.TriggerPush && ts[1].Kind == ir.TriggerTag &&
		!slices.ContainsFunc(ts, func(t ir.Trigger) bool {
			return len(t.Branches.Include)+len(t.Branches.Exclude)+len(t.Tags.Include)+len(t.Tags.Exclude)+len(t.Paths.Include)+len(t.Paths.Exclude) > 0
		})
}

func hasWorkflow(pl *ir.Pipeline) bool {
	m, _ := pl.Ext[GitLab].(map[string]any)
	_, has := m["workflow"]
	return has
}
