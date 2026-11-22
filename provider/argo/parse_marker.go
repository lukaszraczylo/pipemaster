package argo

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const markerIgnored = "marker ignored: "

// markerArg reads the first JSON argument line of the marker for a feature at a path.
func markerArg(head string, path string, f caps.Feature, into any) bool {
	lines := emitkit.ParseArgs([]byte(head))[ir.FeatureUse{IRPath: path, Feature: f}]
	return len(lines) > 0 && json.Unmarshal([]byte(lines[0]), into) == nil
}

func (c *plCtx) ignored(path string, f caps.Feature, why string) {
	c.p.rep.Add(report.Entry{IRPath: path, Feature: f, Level: caps.Lost, Strategy: "kept as written", SourceLoc: c.file, Detail: markerIgnored + why})
}

// inputsMarker puts back the IR input names and types that the emitter changed. A marker that names a parameter
// the file no longer holds is ignored.
func (c *plCtx) inputsMarker(pl *ir.Pipeline) {
	var a inputsArg
	path := "pipeline/" + c.plID
	if !markerArg(c.src.doc.Head, path, caps.PipelineInputs, &a) {
		return
	}
	byName := map[string]int{}
	for i, in := range pl.Inputs {
		byName[in.Name] = i
	}
	for name := range a.Names {
		if _, ok := byName[name]; !ok {
			c.ignored(path, caps.PipelineInputs, "the parameter "+name+" was removed or renamed after pipemaster wrote the file")
			return
		}
	}
	for name := range a.Types {
		if _, ok := byName[name]; !ok {
			c.ignored(path, caps.PipelineInputs, "the parameter "+name+" was removed or renamed after pipemaster wrote the file")
			return
		}
	}
	c.inNames = a.Names
	for name, typ := range a.Types {
		pl.Inputs[byName[name]].Type = typ
	}
	for name, irName := range a.Names {
		pl.Inputs[byName[name]].Name = irName
	}
}

// matrixMarker restores the matrix when the marked withItems still holds the cells that the matrix expands to.
// It reports false when it left the value for Ext.
func (b *jobBuilder) matrixMarker(v any) bool {
	var a matrixArg
	if !markerArg(b.c.src.doc.Head, b.path, caps.JobMatrix, &a) {
		return false
	}
	m := a.matrix()
	if !reflect.DeepEqual(cellMaps(ir.Expand(m)), itemMaps(v)) {
		b.c.ignored(b.path, caps.JobMatrix, "the withItems cells were edited after pipemaster wrote them; the cells stay in Ext")
		return false
	}
	b.job.Matrix = m
	b.tx.matrix = map[string]bool{}
	for _, ax := range m.Axes {
		b.tx.matrix[ax.Name] = true
	}
	for _, combo := range append(append([]ir.Combo{}, m.Include...), m.Exclude...) {
		for _, kv := range combo {
			b.tx.matrix[kv.Key] = true
		}
	}
	return true
}

func cellMaps(cells []ir.Combo) []map[string]string {
	out := make([]map[string]string, 0, len(cells))
	keys := cellKeys(cells)
	for _, c := range cells {
		m := map[string]string{}
		for _, k := range keys {
			m[k] = ""
		}
		for _, kv := range c {
			m[kv.Key] = kv.Value
		}
		out = append(out, m)
	}
	return out
}

// itemMaps reads a withItems list of string maps; nil when an item is not one.
func itemMaps(v any) []map[string]string {
	out := []map[string]string{}
	for _, e := range listOf(v) {
		m, ok := asMap(e)
		if !ok {
			return nil
		}
		cell := map[string]string{}
		for _, k := range m.Order {
			t, ok := text(m.Vals[k])
			if !ok {
				return nil
			}
			cell[k] = t
		}
		out = append(out, cell)
	}
	return out
}

var _ = strings.Join

// gateMarker puts back the approval environment and the manual flag of a job that a suspend task gates.
func (c *plCtx) gateMarker(j *ir.Job) {
	path := "pipeline/" + c.plID + "/job/" + j.ID
	for _, f := range []caps.Feature{caps.JobEnvironmentApproval, caps.JobManual} {
		var a gateArg
		if markerArg(c.src.doc.Head, path, f, &a) {
			j.Manual, j.Environment = a.Manual, a.Environment
			return
		}
	}
}
