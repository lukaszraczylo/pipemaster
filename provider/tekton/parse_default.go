package tekton

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// defaultTriggerIDs lists the pipelines whose file marks their triggers as made from the default events of the source.
func (p *project) defaultTriggerIDs(byName map[string]*ir.Pipeline) (ids []string) {
	for _, src := range p.pipes {
		pl := byName[src.name]
		var a provider.DefaultTriggersArg
		if pl != nil && readMarks(src.doc.Head).arg(caps.TriggerPush, "pipeline/"+pl.ID, &a) && a.Default {
			a.RestoreManual(pl)
			ids = append(ids, pl.ID)
		}
	}
	return ids
}
