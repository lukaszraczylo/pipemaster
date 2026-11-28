package concourse

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// noteDefaultTriggers records that the file marks its triggers as made from the default events of the source.
func (b *pipeBuilder) noteDefaultTriggers(pl *ir.Pipeline) {
	var a provider.DefaultTriggersArg
	if b.defaultTriggers = b.arg(b.path(), caps.TriggerPush, &a) && a.Default; b.defaultTriggers {
		a.RestoreManual(pl)
	}
}
