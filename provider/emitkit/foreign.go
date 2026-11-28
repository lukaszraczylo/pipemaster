package emitkit

import (
	"slices"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// BookkeepingExt names the Ext keys a parser sets only to remember layout it can rebuild: stage lists,
// script sections, derived ids. Losing one changes no behaviour, so no emitter reports it.
var BookkeepingExt = map[string]bool{"stages": true, "stage": true, "section": true, "before_lines": true, "workflow": true, "bash_default": true}

// Meaningful reports whether x holds Ext data of a provider other than own that carries behaviour.
func Meaningful(x ir.Ext, own string) []string {
	var out []string
	for prov, raw := range x {
		if prov == own {
			continue
		}
		if _, isMap := raw.(map[string]any); !isMap || len(meaningfulKeys(raw)) > 0 {
			out = append(out, prov)
		}
	}
	sort.Strings(out)
	return out
}

// ForeignExt reports, for each other provider with meaningful Ext data at path, that it is not written to the
// target file and survives only in round-trip metadata. The entry names the Ext keys, so a reader can tell
// which source settings the target lacks.
func ForeignExt(l *Ledger, own, target, path string, x ir.Ext) {
	for _, prov := range Meaningful(x, own) {
		what := "Ext of provider " + prov
		if keys := meaningfulKeys(x[prov]); len(keys) > 0 {
			what += " (" + strings.Join(keys, ", ") + ")"
		}
		l.Note(ir.FeatureUse{IRPath: path, Feature: caps.ExtPassthrough}, caps.Support{Level: caps.Lost},
			what+" is not written to the "+target+" file; it is kept in round-trip metadata")
	}
	for _, prov := range sortedProviders(x, own) {
		if keys := carriedKeys(x[prov]); len(keys) > 0 {
			l.Retain(ir.FeatureUse{IRPath: path, Feature: caps.ExtPassthrough},
				"Ext of provider "+prov+" ("+strings.Join(keys, ", ")+"): the "+target+" file holds the behaviour; the keys stay in round-trip metadata")
		}
	}
}

func sortedProviders(x ir.Ext, own string) []string {
	var out []string
	for prov := range x {
		if prov != own {
			out = append(out, prov)
		}
	}
	sort.Strings(out)
	return out
}

// carriedSet is the set of Ext keys that the CarriedKey list names.
func carriedSet(m map[string]any) map[string]bool {
	carried := map[string]bool{}
	switch l := m[CarriedKey].(type) {
	case []any:
		for _, v := range l {
			if s, ok := v.(string); ok {
				carried[s] = true
			}
		}
	case []string:
		for _, s := range l {
			carried[s] = true
		}
	}
	return carried
}

// carriedKeys lists the sorted Ext keys of one provider that the target file already holds.
func carriedKeys(raw any) []string {
	m, _ := raw.(map[string]any)
	var out []string
	for k := range carriedSet(m) {
		if _, has := m[k]; has {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Carry adds each key of keys that m holds to the CarriedKey list of m.
func Carry(m map[string]any, keys ...string) {
	var l []any
	switch old := m[CarriedKey].(type) {
	case []any:
		l = old
	case []string:
		for _, s := range old {
			l = append(l, s)
		}
	}
	for _, k := range keys {
		if _, has := m[k]; has && !slices.Contains(l, any(k)) {
			l = append(l, k)
		}
	}
	if len(l) > 0 {
		m[CarriedKey] = l
	}
}

// WithoutCarried returns m without the CarriedKey entry, so an emitter never writes the list into a file.
func WithoutCarried(m map[string]any) map[string]any {
	if _, has := m[CarriedKey]; !has {
		return m
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != CarriedKey {
			out[k] = v
		}
	}
	return out
}

// CarriedKey is the Ext key that lists the sibling keys whose behaviour the target file already holds. The keys
// stay for the round trip and ForeignExt retains them without a report entry.
const CarriedKey = "_carried"

// CheckoutKey is the job Ext key of a source whose job reads the repository of the pipeline but has no checkout step,
// because its provider fetches the repository before the job. A provider that fetches nothing by itself, such as
// GitHub Actions, writes the checkout. The value holds the fetch depth under "depth", when the source set one.
const CheckoutKey = "_checkout"

// meaningfulKeys lists the sorted Ext keys of one provider that carry behaviour.
func meaningfulKeys(raw any) []string {
	m, _ := raw.(map[string]any)
	carried := carriedSet(m)
	var out []string
	for k := range m {
		if !BookkeepingExt[k] && !carried[k] && !strings.HasPrefix(k, "_") {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
