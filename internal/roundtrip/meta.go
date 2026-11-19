package roundtrip

import (
	"fmt"
	"strings"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

// NewMeta records what emitting proj through target loses: Ext blobs, renamed IDs, display names, action
// steps and the fields rep marks lost or external.
func NewMeta(sourceID string, proj *ir.Project, target provider.Provider, rep *report.Report) *meta.Meta {
	ids := IDsFor(target, proj)
	lost, skipped := meta.CollectLost(proj, sourceEntries(rep, ids))
	noteUnrecorded(rep, ids, skipped)
	return &meta.Meta{
		IDs:      ids,
		Lost:     lost,
		Source:   sourceID,
		Dialect:  proj.Origin.Dialect,
		Ext:      meta.Collect(proj),
		Residual: meta.CollectResidual(proj),
		Version:  meta.Version,
	}
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
	RestoreIDs(proj, m.IDs)
	if m.Source != "" {
		proj.Origin.Provider, proj.Origin.Dialect = m.Source, m.Dialect
	}
	if keepEdits {
		return meta.ReattachKeepingEdits(proj, m)
	}
	return meta.Reattach(proj, m), nil
}

// sourceEntries rewrites report paths under a renamed job to the source job ID.
func sourceEntries(rep *report.Report, ids map[string]string) []report.Entry {
	if rep == nil {
		return nil
	}
	out := make([]report.Entry, len(rep.Entries))
	copy(out, rep.Entries)
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
