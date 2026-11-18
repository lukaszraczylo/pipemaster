package emitkit

import (
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// BookkeepingExt names the Ext keys a parser sets only to remember layout it can rebuild: stage lists,
// script sections, derived ids. Losing one changes no behaviour, so no emitter reports it.
var BookkeepingExt = map[string]bool{"stages": true, "stage": true, "section": true, "before_lines": true, "workflow": true}

// Meaningful reports whether x holds Ext data of a provider other than own that carries behaviour.
func Meaningful(x ir.Ext, own string) []string {
	var out []string
	for prov, raw := range x {
		if prov == own {
			continue
		}
		m, isMap := raw.(map[string]any)
		if !isMap {
			out = append(out, prov)
			continue
		}
		for k := range m {
			if !BookkeepingExt[k] && !strings.HasPrefix(k, "_") {
				out = append(out, prov)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// ForeignExt reports, for each other provider with meaningful Ext data at path, that it is not written to the
// target file and survives only in round-trip metadata.
func ForeignExt(l *Ledger, own, target, path string, x ir.Ext) {
	for _, prov := range Meaningful(x, own) {
		l.Note(ir.FeatureUse{IRPath: path, Feature: caps.ExtPassthrough}, caps.Support{Level: caps.Lost},
			"Ext of provider "+prov+" is not written to the "+target+" file; it is kept in round-trip metadata")
	}
}
