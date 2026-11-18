package roundtrip

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

// NewMeta records what emitting proj through target loses: Ext blobs, renamed IDs, display names, action
// steps and the fields rep marks lost or external.
func NewMeta(sourceID string, proj *ir.Project, target provider.Provider, rep *report.Report) *meta.Meta {
	ids := IDsFor(target, proj)
	return &meta.Meta{
		IDs:      ids,
		Lost:     meta.CollectLost(proj, sourceEntries(rep, ids)),
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
	RestoreIDs(proj, m.IDs)
	if m.Source != "" {
		proj.Origin.Provider, proj.Origin.Dialect = m.Source, m.Dialect
	}
	return meta.Reattach(proj, m)
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
