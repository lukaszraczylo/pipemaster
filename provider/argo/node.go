package argo

import (
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

type (
	omap   = ynode.Map
	scal   = ynode.Scal
	reader = ynode.Reader
	rawDoc = ynode.Doc
	sink   = ynode.Sink
)

var (
	newReader     = ynode.NewReader
	asMap         = ynode.AsMap
	text          = ynode.Text
	textList      = ynode.TextList
	boolOf        = ynode.BoolOf
	textOf        = ynode.TextOf
	scalOf        = ynode.ScalOf
	plain         = ynode.Plain
	valueOf       = ynode.ValueOf
	stringList    = ynode.StringList
	parseDuration = ynode.Duration
	metaRest      = ynode.MetaRest
)

func newSink(rep *report.Report, file, path string) *sink {
	return ynode.NewSink(dialect, rep, file, path)
}

const idPrefix = ynode.IDPrefix
