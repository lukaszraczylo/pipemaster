// Package roundtrip keeps job and pipeline IDs recoverable across a conversion and back.
package roundtrip

import (
	"strconv"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// IDMapper is implemented by emitters that rename IDs. EmittedIDs maps the IR path of each renamed
// pipeline or job as the target parses it (pipeline/<id>, pipeline/<id>/job/<id>) to its source ID.
type IDMapper interface {
	EmittedIDs(p *ir.Project) map[string]string
}

// IDsFor returns the ID map of p when emitted through t; nil when t renames nothing.
func IDsFor(t provider.Provider, p *ir.Project) map[string]string {
	if m, ok := t.(IDMapper); ok {
		return m.EmittedIDs(p)
	}
	return nil
}

// SanitizeID makes id valid for target. GHA job ids match ^[A-Za-z_][A-Za-z0-9_-]*$; other targets keep id.
// It does not dedupe; AssignIDs does.
func SanitizeID(target, id string) string {
	if target != provider.GHA {
		return id
	}
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

// AssignIDs maps each id to a valid, unique id for target. Ids already valid keep their name; the others
// are sanitised in order and take -2, -3 on collision, so the result never depends on map order.
func AssignIDs(target string, ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	taken := map[string]bool{}
	for _, id := range ids {
		if SanitizeID(target, id) == id {
			out[id], taken[id] = id, true
		}
	}
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		base := SanitizeID(target, id)
		cand := base
		for n := 2; taken[cand]; n++ {
			cand = base + "-" + strconv.Itoa(n)
		}
		out[id], taken[cand] = cand, true
	}
	return out
}

// RestoreIDs renames pipelines and jobs back to the source IDs in ids, and rewrites needs to match.
// Renames apply at once, so swapped names stay correct. Run it before meta.Reattach: Ext paths use source IDs.
func RestoreIDs(p *ir.Project, ids map[string]string) {
	if p == nil || len(ids) == 0 {
		return
	}
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		old := pl.ID
		jobs := map[string]string{}
		for _, j := range allJobs(pl) {
			if src, ok := ids["pipeline/"+old+"/job/"+j.ID]; ok {
				jobs[j.ID] = src
			}
		}
		for _, j := range allJobs(pl) {
			if src, ok := jobs[j.ID]; ok {
				j.ID = src
			}
			for i := range j.Needs {
				if src, ok := jobs[j.Needs[i].JobID]; ok {
					j.Needs[i].JobID = src
				}
			}
		}
		if src, ok := ids["pipeline/"+old]; ok {
			pl.ID = src
		}
	}
}

func allJobs(pl *ir.Pipeline) []*ir.Job {
	var out []*ir.Job
	for _, js := range [][]*ir.Job{pl.Jobs, pl.Finally} {
		for _, j := range js {
			if j != nil {
				out = append(out, j)
			}
		}
	}
	return out
}
