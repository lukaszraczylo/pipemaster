package roundtrip

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/report"
)

// NewMeta records what emitting proj through target loses: Ext blobs, renamed IDs, display names, action
// steps and the fields rep marks lost or external.
func NewMeta(sourceID string, proj *ir.Project, target provider.Provider, rep *report.Report) *meta.Meta {
	proj, inlined := inline.Flatten(proj, target.ID())
	ids := IDsFor(target, proj)
	lost, skipped := meta.CollectLost(proj, append(sourceEntries(rep, ids), retainedEntries(rep, ids)...))
	noteUnrecorded(rep, ids, skipped)
	m := &meta.Meta{
		IDs:      ids,
		Lost:     lost,
		Source:   sourceID,
		Target:   target.ID(),
		Dialect:  proj.Origin.Dialect,
		Ext:      meta.Collect(proj),
		Residual: meta.CollectResidual(proj),
		Calls:    nilIfEmpty(inlined.Calls),
		Version:  meta.Version,
	}
	if g := proj.Origin.Guarded; g != nil {
		m.GuardScoped = true
		m.Guarded = slices.Sorted(maps.Keys(g))
	}
	return m
}

func nilIfEmpty(c map[string]meta.Call) map[string]meta.Call {
	if len(c) == 0 {
		return nil
	}
	return c
}

// Restore undoes the renames in m, then re-attaches its Ext, names and lost fields. It returns the stored
// paths that match no node. The target file was emitted from the source project, so every expression in
// proj, stored or parsed, now reads in the source dialect and the emitter must not translate it again.
func Restore(proj *ir.Project, m *meta.Meta) []string {
	orphans, _ := restore(proj, m, false)
	return orphans
}

// RestoreKeepingEdits is Restore for a file edited after conversion, where the edited content wins: stored
// values fill only what the parsed node lacks. It also returns the paths where a stored value was not applied.
func RestoreKeepingEdits(proj *ir.Project, m *meta.Meta) (orphans, kept []string) {
	return restore(proj, m, true)
}

func restore(proj *ir.Project, m *meta.Meta, keepEdits bool) (orphans, kept []string) {
	if m.Source != "" {
		dropDefaultTriggers(proj)
	}
	RestoreIDs(proj, m.IDs)
	if m.Source != "" {
		proj.Origin.Provider, proj.Origin.Dialect = m.Source, m.Dialect
	}
	if m.GuardScoped {
		proj.Origin.Guarded = map[string]bool{}
		for _, p := range m.Guarded {
			proj.Origin.Guarded[p] = true
		}
	}
	if keepEdits {
		orphans, kept = meta.ReattachKeepingEdits(proj, m)
		// The edited jobs win, so the inlined jobs stay as they are instead of collapsing into the stored call.
		kept = append(kept, slices.Sorted(maps.Keys(m.Calls))...)
		return orphans, kept
	}
	orphans = meta.Reattach(proj, m)
	return append(orphans, restoreCalls(proj, m.Calls)...), nil
}

// dropDefaultTriggers removes the triggers that an emitter made from the default events of a Drone or GitLab
// source. The metadata names that source, so the pipeline reads back with no trigger. It runs before RestoreIDs,
// while the pipeline IDs are the parsed ones.
func dropDefaultTriggers(proj *ir.Project) {
	for _, pl := range proj.Pipelines {
		if pl != nil && proj.Origin.DefaultTriggers[pl.ID] {
			pl.Triggers = nil
		}
	}
	// One pass only: a later layer restores triggers that this pass must not drop again.
	proj.Origin.DefaultTriggers = nil
}

// restoreCalls replaces the jobs that an inline wrote with the stored call job and its child pipeline, and puts
// back the needs of the jobs that the inline rewired. It returns the call paths it could not restore.
func restoreCalls(proj *ir.Project, calls map[string]meta.Call) (orphans []string) {
	for _, path := range slices.Sorted(maps.Keys(calls)) {
		c := calls[path]
		pid, _, ok := strings.Cut(strings.TrimPrefix(path, "pipeline/"), "/job/")
		var pl *ir.Pipeline
		if ok && proj != nil {
			for _, p := range proj.Pipelines {
				if p != nil && p.ID == pid {
					pl = p
				}
			}
		}
		if pl == nil || c.Job == nil || !hasAll(pl, c.Inlined) {
			orphans = append(orphans, path)
			continue
		}
		drop := func(j *ir.Job) bool { return j == nil || j.ID == c.Job.ID || slices.Contains(c.Inlined, j.ID) }
		pl.Jobs = slices.DeleteFunc(pl.Jobs, drop)
		pl.Finally = slices.DeleteFunc(pl.Finally, drop)
		call := ir.Clone(&ir.Project{Pipelines: []*ir.Pipeline{{Jobs: []*ir.Job{c.Job}}}}).Pipelines[0].Jobs[0]
		pl.Jobs = append(pl.Jobs, call)
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			if was, ok := c.Needs[j.ID]; ok {
				j.Needs = slices.Clone(was)
			}
		}
	}
	return orphans
}

func hasAll(pl *ir.Pipeline, ids []string) bool {
	have := map[string]bool{}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		if j != nil {
			have[j.ID] = true
		}
	}
	return !slices.ContainsFunc(ids, func(id string) bool { return !have[id] })
}

// sourceEntries rewrites report paths under a renamed job to the source job ID.
func sourceEntries(rep *report.Report, ids map[string]string) []report.Entry {
	if rep == nil {
		return nil
	}
	return renamed(rep.Entries, ids)
}

// retainedEntries is sourceEntries for the fields the metadata keeps without a report entry.
func retainedEntries(rep *report.Report, ids map[string]string) []report.Entry {
	if rep == nil {
		return nil
	}
	return renamed(rep.Retained, ids)
}

func renamed(entries []report.Entry, ids map[string]string) []report.Entry {
	out := make([]report.Entry, len(entries))
	copy(out, entries)
	for i, e := range out {
		for emitted, src := range ids {
			rest, ok := strings.CutPrefix(e.IRPath, emitted)
			if !ok || (rest != "" && rest[0] != '/') {
				continue
			}
			head, _, _ := strings.Cut(emitted, "/job/")
			out[i].IRPath = head + "/job/" + src + rest
		}
	}
	return out
}

// noteUnrecorded says in the report which lost features the metadata cannot return.
func noteUnrecorded(rep *report.Report, ids map[string]string, skipped []meta.Unrecorded) {
	if rep == nil {
		return
	}
	src := sourceEntries(rep, ids)
	for _, u := range skipped {
		for i, e := range src {
			if e.IRPath == u.Path && e.Feature == u.Feature {
				rep.Entries[i].Detail += "; not kept in round-trip metadata: " + u.Reason
			}
		}
	}
}

// RecordEmitted stores in m what the emitted files read back as for every lost field, so a later restore of an
// edited file can tell a field the user changed from one that still reads as emitted. It reads files with
// target's parser before any metadata is attached.
func RecordEmitted(m *meta.Meta, target provider.Provider, files map[string][]byte) error {
	if len(m.Lost) == 0 {
		return nil
	}
	fsys := fstest.MapFS{}
	for name, b := range files {
		fsys[name] = &fstest.MapFile{Data: b}
	}
	parsed, _, err := target.Parse(fsys)
	if err != nil {
		return fmt.Errorf("read back %s output: %w", target.ID(), err)
	}
	RestoreIDs(parsed, m.IDs)
	m.Emitted = meta.CollectEmitted(parsed, m.Lost)
	return nil
}
