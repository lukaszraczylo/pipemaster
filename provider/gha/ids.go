package gha

import (
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// jobIDMap maps each job id of pl to the valid id it is emitted under.
func jobIDMap(pl *ir.Pipeline) map[string]string {
	var ids []string
	for _, js := range [][]*ir.Job{pl.Jobs, pl.Finally} {
		for _, j := range js {
			if j != nil {
				ids = append(ids, j.ID)
			}
		}
	}
	return roundtrip.AssignIDs(provider.GHA, ids)
}

// idRename is one job id the emitter changed, as the path of the renamed job and the original id.
type idRename struct{ path, from, to string }

// withValidJobIDs returns p with job ids, needs and needs references renamed to GHA-valid ids, and the
// renames it made. The input is never written to: a project that needs renames is cloned first.
func withValidJobIDs(p *ir.Project) (*ir.Project, []idRename) {
	if p == nil {
		return nil, nil
	}
	var plans []map[string]string
	changed := false
	for _, pl := range p.Pipelines {
		var ids map[string]string
		if pl != nil {
			ids = jobIDMap(pl)
		}
		plans = append(plans, ids)
		changed = changed || renames(ids)
	}
	if !changed {
		return p, nil
	}
	var done []idRename
	out := ir.Clone(p)
	for i, pl := range out.Pipelines {
		if pl == nil || !renames(plans[i]) {
			continue
		}
		ids := plans[i]
		emitkit.RewriteNeedsRefs(pl, renamed(ids))
		for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
			if j == nil {
				continue
			}
			if ids[j.ID] != j.ID {
				done = append(done, idRename{path: jobPath(pl.ID, ids[j.ID]), from: j.ID, to: ids[j.ID]})
			}
			j.ID = ids[j.ID]
			for k, n := range j.Needs {
				if id, ok := ids[n.JobID]; ok {
					j.Needs[k].JobID = id
				}
			}
		}
	}
	return out, done
}

func renames(ids map[string]string) bool { return len(renamed(ids)) > 0 }

// renamed keeps only the ids that change.
func renamed(ids map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range ids {
		if k != v {
			out[k] = v
		}
	}
	return out
}

// EmittedIDs lists the jobs whose id Emit changes, keyed by the IR path Parse gives them.
func (a *adapter) EmittedIDs(p *ir.Project) map[string]string {
	if p == nil {
		return nil
	}
	out := map[string]string{}
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		for src, id := range jobIDMap(pl) {
			if src != id {
				out["pipeline/"+pl.ID+"/job/"+id] = src
			}
		}
	}
	return out
}
