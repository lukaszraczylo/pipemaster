package roundtrip

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
	"github.com/lukaszraczylo/pipemaster/report"
)

// ReturnLayer is the metadata layer that a conversion writes, or nil when it writes none. A full return emits
// into the origin dialect, which can still lack a key the pipeline holds; no older layer keeps that key, so
// this output records it. The convert and roundtrip commands share it.
func ReturnLayer(res HopResult, proj *ir.Project, src, tgt provider.Provider, emitRep *report.Report) *meta.Meta {
	fullReturn := res.Restored && len(res.Out.Layers) == 0
	keys := unwrittenKeyExt(proj, tgt.ID())
	if res.Restored && (!fullReturn || (!dialectLoss(emitRep) && len(keys) == 0)) {
		return nil
	}
	owner := src.ID()
	if res.Restored {
		owner = tgt.ID()
	}
	m := NewMeta(owner, proj, tgt, emitRep)
	if res.Restored {
		m.Residual, m.IDs = nil, nil
		m.Ext = keys
	}
	return m
}

// dialectLoss reports whether the emit dropped a pipeline feature. Foreign Ext that the output never holds is not
// one: it never belonged to the origin. An external verdict asks for setup, it drops nothing.
func dialectLoss(rep *report.Report) bool {
	return slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
		return e.Level == caps.Lost && e.Feature != caps.ExtPassthrough
	})
}

// unwrittenKeyExt holds the Concourse Ext of each pipeline that has top-level keys the Concourse emitter does
// not write. The origin file lacks them, and no older layer is left to return them.
func unwrittenKeyExt(proj *ir.Project, target string) map[string]ir.Ext {
	if target != provider.Concourse {
		return nil
	}
	var out map[string]ir.Ext
	for _, pl := range proj.Pipelines {
		ext, _ := pl.Ext[provider.Concourse].(map[string]any)
		if extra, _ := ext[concourse.ExtExtra].(map[string]any); len(extra) > 0 {
			if out == nil {
				out = map[string]ir.Ext{}
			}
			out["pipeline/"+pl.ID] = ir.Ext{provider.Concourse: ext}
		}
	}
	return out
}
