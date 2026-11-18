package ir

import (
	"fmt"
	"sort"
	"strings"
)

// Normalize puts every order-insensitive collection into canonical order in place.
// Steps, Env, Script, Matrix axes, Matrix include/exclude and Filter lists keep their order:
// a later "!" pattern overrides an earlier one, so filter order is semantic.
func Normalize(p *Project) {
	if p == nil {
		return
	}
	sort.SliceStable(p.Pipelines, func(i, j int) bool { return pipelineID(p.Pipelines[i]) < pipelineID(p.Pipelines[j]) })
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		normalizeTriggers(pl.Triggers)
		sortJobs(pl.Jobs)
		sortJobs(pl.Finally)
		for _, j := range pl.Jobs {
			normalizeJob(j)
		}
		for _, j := range pl.Finally {
			normalizeJob(j)
		}
	}
}

func pipelineID(p *Pipeline) string {
	if p == nil {
		return ""
	}
	return p.ID
}

func sortJobs(js []*Job) {
	sort.SliceStable(js, func(i, j int) bool { return jobID(js[i]) < jobID(js[j]) })
}

func jobID(j *Job) string {
	if j == nil {
		return ""
	}
	return j.ID
}

func normalizeJob(j *Job) {
	if j == nil {
		return
	}
	sort.SliceStable(j.Needs, func(a, b int) bool { return j.Needs[a].JobID < j.Needs[b].JobID })
	sort.Strings(j.Runner.Labels)
	if j.Matrix != nil {
		for _, set := range [][]Combo{j.Matrix.Include, j.Matrix.Exclude} {
			for _, c := range set {
				sort.SliceStable(c, func(a, b int) bool { return c[a].Key < c[b].Key })
			}
		}
	}
}

// triggerKey makes the sort total so equal sets in any input order normalise identically.
func triggerKey(t *Trigger) string {
	var b strings.Builder
	b.WriteString(string(t.Kind))
	for _, f := range []Filter{t.Branches, t.Tags, t.Paths} {
		b.WriteString("\x00")
		b.WriteString(strings.Join(f.Include, "\x01"))
		b.WriteString("\x02")
		b.WriteString(strings.Join(f.Exclude, "\x01"))
	}
	b.WriteString("\x00" + t.Cron)
	if len(t.Ext) > 0 {
		b.WriteString("\x00" + fmt.Sprint(t.Ext))
	}
	if t.Resource != nil {
		b.WriteString("\x00" + t.Resource.Type + "\x01" + t.Resource.Name)
	}
	return b.String()
}

func normalizeTriggers(ts []Trigger) {
	sort.SliceStable(ts, func(a, b int) bool { return triggerKey(&ts[a]) < triggerKey(&ts[b]) })
}
