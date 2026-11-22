package tekton

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider/internal/ymap"
)

type (
	wmap   = ymap.Map
	outDoc = ymap.Doc
)

var (
	newMap      = ymap.New
	str         = ymap.Str
	boolNode    = ymap.Bool
	intNode     = ymap.Int
	seqOf       = ymap.Seq
	strSeq      = ymap.StrSeq
	block       = ymap.Block
	nameValue   = ymap.NameValue
	plainNode   = ymap.PlainNode
	marshalDocs = ymap.Marshal
)

func sortedKeys[V any](m map[string]V) []string { return ymap.SortedKeys(m) }

func extOf(e map[string]any) map[string]any {
	m, _ := e[dialect].(map[string]any)
	return m
}

// commentSafe makes s fit on one comment line.
func commentSafe(s string) string {
	s = strings.ReplaceAll(s, "${{", "{{")
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}
