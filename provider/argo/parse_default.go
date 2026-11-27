package argo

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// dropDefaultTriggers restores a pipeline with no trigger when the file marks its triggers as made from the
// default events of the source.
func (p *project) dropDefaultTriggers(pls []*ir.Pipeline, srcOf map[*ir.Pipeline]*pipeSrc) {
	for _, pl := range pls {
		src := srcOf[pl]
		var a provider.DefaultTriggersArg
		if src != nil && markerArg(src.doc.Head, "pipeline/"+pl.ID, caps.TriggerPush, &a) && a.Default {
			pl.Triggers = nil
		}
	}
}
