package gha

import (
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
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

// withValidJobIDs returns p with job ids and needs renamed to GHA-valid ids. Unchanged pipelines and
// jobs are shared with p, which is never written to.
func withValidJobIDs(p *ir.Project) *ir.Project {
	if p == nil {
		return nil
	}
	out := *p
	out.Pipelines = make([]*ir.Pipeline, len(p.Pipelines))
	for i, pl := range p.Pipelines {
		out.Pipelines[i] = pl
		if pl == nil {
			continue
		}
		ids := jobIDMap(pl)
		if !renames(ids) {
			continue
		}
		cp := *pl
		cp.Jobs, cp.Finally = renameJobs(pl.Jobs, ids), renameJobs(pl.Finally, ids)
		out.Pipelines[i] = &cp
	}
	return &out
}

func renames(ids map[string]string) bool {
	for k, v := range ids {
		if k != v {
			return true
		}
	}
	return false
}

func renameJobs(jobs []*ir.Job, ids map[string]string) []*ir.Job {
	out := make([]*ir.Job, len(jobs))
	for i, j := range jobs {
		if j == nil {
			continue
		}
		cp := *j
		cp.ID = ids[j.ID]
		cp.Needs = make([]ir.Need, len(j.Needs))
		for k, n := range j.Needs {
			if id, ok := ids[n.JobID]; ok {
				n.JobID = id
			}
			cp.Needs[k] = n
		}
		out[i] = &cp
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
