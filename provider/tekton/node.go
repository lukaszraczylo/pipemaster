package tekton

import (
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

type (
	omap   = ynode.Map
	scal   = ynode.Scal
	reader = ynode.Reader
	rawDoc = ynode.Doc
)

const idPrefix = ynode.IDPrefix

var (
	newReader      = ynode.NewReader
	asMap          = ynode.AsMap
	text           = ynode.Text
	textList       = ynode.TextList
	boolOf         = ynode.BoolOf
	textOf         = ynode.TextOf
	scalOf         = ynode.ScalOf
	plain          = ynode.Plain
	readDocs       = ynode.ReadDocs
	valueOf        = ynode.ValueOf
	stringList     = ynode.StringList
	parseDuration  = ynode.Duration
	containerPorts = ynode.ContainerPorts
)

// readDocsBytes reads documents that the emitter built in memory.
func readDocsBytes(data []byte) ([]*rawDoc, error) {
	return ynode.ReadDocsData(data, "", &report.Report{})
}
