package argo

import (
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ymap"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
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
	return emitkit.WithoutCarried(m)
}

// commentSafe makes s fit on one comment line.
func commentSafe(s string) string {
	s = strings.ReplaceAll(s, "${{", "{{")
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}

// idComment is the head comment that carries the IR ID of a node whose written name differs.
func idComment(id string) string { return "# " + idPrefix + commentSafe(id) }

// readDocsBytes reads documents that the emitter built in memory.
func readDocsBytes(data []byte) ([]*rawDoc, error) {
	return ynode.ReadDocsData(data, "", &report.Report{})
}

func itoa(i int) string { return strconv.Itoa(i) }
