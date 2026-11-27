package ynode

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Sink collects the keys a parser could not map; they become Ext[dialect] and each has a report entry.
type Sink struct {
	Rep     *report.Report
	Out     map[string]any
	Dialect string
	Path    string
	File    string
	// Carried lists the kept keys whose behaviour the IR already holds.
	Carried []string
}

// NewSink starts a sink for one IR path of a file.
func NewSink(dialect string, rep *report.Report, file, path string) *Sink {
	return &Sink{Rep: rep, Out: map[string]any{}, Dialect: dialect, Path: path, File: file}
}

// Strategy is the report strategy of a value that stays in Ext.
func (k *Sink) Strategy() string { return "kept in Ext[" + k.Dialect + "]" }

// Loc is the source location of a line of the file.
func (k *Sink) Loc(line int) string {
	if line > 0 {
		return fmt.Sprintf("%s:%d", k.File, line)
	}
	return k.File
}

// Note adds a lost entry whose data stays in Ext.
func (k *Sink) Note(f caps.Feature, detail string, line int) {
	k.Rep.Add(report.Entry{IRPath: k.Path, Feature: f, Level: caps.Lost, Strategy: k.Strategy(), Detail: detail, SourceLoc: k.Loc(line)})
}

// Keep stores v under key and reports why.
func (k *Sink) Keep(key string, v any, f caps.Feature, detail string, line int) {
	k.Out[key] = Plain(v)
	k.Note(f, detail, line)
}

// KeepRest moves every key the reader did not consume to Ext.
func (k *Sink) KeepRest(r *Reader) { k.KeepRestAs(r, "") }

// KeepRestAs is KeepRest with a key prefix, for the keys of a body that share an Ext with its parent.
func (k *Sink) KeepRestAs(r *Reader, prefix string) {
	for _, key := range r.Rest() {
		k.Keep(prefix+key, r.M.Vals[key], caps.ExtPassthrough, "key "+key+" has no IR field; kept in Ext", r.Line(key))
	}
}

// Ext is the kept keys as the Ext of an IR node; no key leaves no Ext.
func (k *Sink) Ext() ir.Ext {
	if len(k.Out) == 0 {
		return nil
	}
	emitkit.Carry(k.Out, k.Carried...)
	return ir.Ext{k.Dialect: k.Out}
}

// Carry marks kept keys as held by the IR; a key that Keep did not store is ignored.
func (k *Sink) Carry(keys ...string) { k.Carried = append(k.Carried, keys...) }

// KeepMeta keeps the metadata of a document except its name.
func (k *Sink) KeepMeta(key string, top *Map, what string, line int) {
	meta, _ := AsMap(top.Vals["metadata"])
	if rest := MetaRest(meta); len(rest.Order) > 0 {
		k.Keep(key, rest, caps.ExtPassthrough, what+" metadata has no IR field; kept in Ext", line)
	}
}

// MetaRest is the metadata keys except the name.
func MetaRest(meta *Map) *Map {
	rest := NewMap()
	if meta == nil {
		return rest
	}
	for _, key := range meta.Order {
		if key != "name" {
			rest.Set(key, meta.Vals[key], meta.Lines[key])
		}
	}
	return rest
}
