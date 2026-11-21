package tekton

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	strategyExt = "kept in Ext[" + dialect + "]"

	// ExtResources is the Ext key of the first pipeline that holds the documents no IR field models, in file order.
	ExtResources = "resources"
	// ExtImplicitNeeds is the job Ext key that lists the needs that came only from a result or status reference.
	// The emitter writes no runAfter for them, because the reference orders the tasks.
	ExtImplicitNeeds = "implicit_needs"
	// ExtCommand is the step Ext key that keeps the command list of a step that the IR holds as one script line.
	ExtCommand = "command"
	// ExtArgs is the step Ext key that keeps the args list beside ExtCommand.
	ExtArgs = "args"
)

// sink collects the keys a parser could not map; they become Ext[tekton] and each has a report entry.
type sink struct {
	rep  *report.Report
	vals map[string]any
	path string
	file string
}

func newSink(rep *report.Report, file, path string) *sink {
	return &sink{rep: rep, vals: map[string]any{}, path: path, file: file}
}

func (k *sink) loc(line int) string {
	if line > 0 {
		return fmt.Sprintf("%s:%d", k.file, line)
	}
	return k.file
}

// note adds a lost entry whose data stays in Ext.
func (k *sink) note(f caps.Feature, detail string, line int) {
	k.rep.Add(report.Entry{IRPath: k.path, Feature: f, Level: caps.Lost, Strategy: strategyExt, Detail: detail, SourceLoc: k.loc(line)})
}

// keep stores v under key and reports why.
func (k *sink) keep(key string, v any, f caps.Feature, detail string, line int) {
	k.vals[key] = plain(v)
	k.note(f, detail, line)
}

// keepRest moves every key the reader did not consume to Ext.
func (k *sink) keepRest(r *reader) {
	for _, key := range r.rest() {
		k.keep(key, r.m.vals[key], caps.ExtPassthrough, "key "+key+" has no IR field; kept in Ext", r.line(key))
	}
}

func (k *sink) ext() ir.Ext {
	if len(k.vals) == 0 {
		return nil
	}
	return ir.Ext{dialect: k.vals}
}
