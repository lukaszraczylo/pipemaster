package gha

import (
	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// SanitizeID makes id match the GHA job id pattern ^[A-Za-z_][A-Za-z0-9_-]*$; Gitea and Forgejo share it.
func (*adapter) SanitizeID(id string) string {
	out := make([]byte, 0, len(id)+1)
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !validIDByte(c, false) {
			c = '-'
		}
		out = append(out, c)
	}
	if len(out) == 0 || !validIDByte(out[0], true) {
		out = append([]byte{'_'}, out...)
	}
	return string(out)
}

func validIDByte(c byte, first bool) bool {
	letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
	return letter || !first && (c >= '0' && c <= '9' || c == '-')
}

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

// stepIDMap maps each step id of j to the valid, unique id it is written under. Made-up ids are not written.
func stepIDMap(j *ir.Job) map[string]string {
	var ids []string
	for _, st := range j.Steps {
		if st != nil && st.ID != "" && !derivedStep(st, nil) {
			ids = append(ids, st.ID)
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
		jobs := jobIDMap(pl)
		for src, id := range jobs {
			if src != id {
				out["pipeline/"+pl.ID+"/job/"+id] = src
			}
		}
		for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
			if j == nil {
				continue
			}
			for src, id := range stepIDMap(j) {
				if src != id {
					out["pipeline/"+pl.ID+"/job/"+jobs[j.ID]+"/step/"+id] = src
				}
			}
		}
	}
	return out
}
