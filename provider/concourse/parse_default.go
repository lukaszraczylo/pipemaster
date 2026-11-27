package concourse

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// dropDefaultTriggers restores a pipeline with no trigger when the file marks its triggers as made from the
// default events of the source.
func (b *pipeBuilder) dropDefaultTriggers(pl *ir.Pipeline) {
	var a provider.DefaultTriggersArg
	if b.arg(b.path(), caps.TriggerPush, &a) && a.Default {
		pl.Triggers = nil
	}
}
