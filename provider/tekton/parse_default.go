package tekton

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// dropDefaultTriggers restores a pipeline with no trigger when the file marks its triggers as made from the
// default events of the source.
func (p *project) dropDefaultTriggers(byName map[string]*ir.Pipeline) {
	for _, src := range p.pipes {
		pl := byName[src.name]
		var a provider.DefaultTriggersArg
		if pl != nil && readMarks(src.doc.Head).arg(caps.TriggerPush, "pipeline/"+pl.ID, &a) && a.Default {
			pl.Triggers = nil
		}
	}
}
