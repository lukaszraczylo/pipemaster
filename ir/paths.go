package ir

import "strconv"

func triggerPath(pid string, i int) string { return pipelinePath(pid) + "/trigger/" + strconv.Itoa(i) }
func stepPath(pid, jid, sid string) string { return jobPath(pid, jid) + "/step/" + sid }

// ExtNode is one IR node's Ext field with its IR path.
type ExtNode struct {
	Ext  *Ext
	Path string
}

// ExtNodes lists every pipeline, trigger, job (Finally included) and step Ext in a fixed order.
// Trigger indexes follow slice order, so call Normalize first for stable paths.
func ExtNodes(p *Project) []ExtNode {
	var out []ExtNode
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		out = append(out, ExtNode{&pl.Ext, pipelinePath(pl.ID)})
		for i := range pl.Triggers {
			out = append(out, ExtNode{&pl.Triggers[i].Ext, triggerPath(pl.ID, i)})
		}
		for _, jobs := range [][]*Job{pl.Jobs, pl.Finally} {
			for _, j := range jobs {
				if j == nil {
					continue
				}
				out = append(out, ExtNode{&j.Ext, jobPath(pl.ID, j.ID)})
				for _, s := range j.Steps {
					if s != nil {
						out = append(out, ExtNode{&s.Ext, stepPath(pl.ID, j.ID, s.ID)})
					}
				}
			}
		}
	}
	return out
}
