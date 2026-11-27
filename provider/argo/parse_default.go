package argo

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// defaultTriggerIDs lists the pipelines whose file marks their triggers as made from the default events of the source.
func (p *project) defaultTriggerIDs(pls []*ir.Pipeline, srcOf map[*ir.Pipeline]*pipeSrc) (ids []string) {
	for _, pl := range pls {
		src := srcOf[pl]
		var a provider.DefaultTriggersArg
		if src != nil && markerArg(src.doc.Head, "pipeline/"+pl.ID, caps.TriggerPush, &a) && a.Default {
			a.RestoreManual(pl)
			ids = append(ids, pl.ID)
		}
	}
	return ids
}
