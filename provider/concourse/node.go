package concourse

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
)

type (
	omap   = ynode.Map
	reader = ynode.Reader
)

var (
	newReader     = ynode.NewReader
	asMap         = ynode.AsMap
	text          = ynode.Text
	textList      = ynode.TextList
	boolOf        = ynode.BoolOf
	plain         = ynode.Plain
	valueOf       = ynode.ValueOf
	stringList    = ynode.StringList
	parseDuration = ynode.Duration
	textOf        = ynode.TextOf
	omapNew       = ynode.NewMap
)

// keepAll keeps every unconsumed key of a step in Ext and reports them as one entry.
func keepAll(k *sink, r *reader, f caps.Feature, detail string, line int) {
	for _, key := range r.Rest() {
		k.Out[key] = plain(r.M.Vals[key])
	}
	k.Note(f, detail, line)
}
