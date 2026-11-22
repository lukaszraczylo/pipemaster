package argo

import (
	"encoding/json"
	"reflect"
	"slices"
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

// staleGates reports a gate marker whose suspend task the file no longer holds. The job stays as written.
func (c *plCtx) staleGates(jobs []*ir.Job) {
	for _, j := range jobs {
		if j.Manual || j.Environment != nil {
			continue
		}
		path := "pipeline/" + c.plID + "/job/" + j.ID
		for _, f := range []caps.Feature{caps.JobEnvironmentApproval, caps.JobManual} {
			var a gateArg
			if markerArg(c.src.doc.Head, path, f, &a) {
				c.ignored(path, f, "the suspend task of the gate was removed or edited after pipemaster wrote the file; the job runs without a gate")
			}
		}
	}
}

// sameEnv finds the variable name in every script step. ok is false when a step lacks it or two steps differ.
func sameEnv(steps []*ir.Step, name string) (v ir.Var, ok bool) {
	for _, st := range steps {
		i := slices.IndexFunc(st.Env, func(o ir.Var) bool { return o.Name == name })
		if i < 0 || (ok && !reflect.DeepEqual(v, st.Env[i])) {
			return ir.Var{}, false
		}
		v, ok = st.Env[i], true
	}
	return v, ok
}

// liftEnv moves the marked variables out of every script step and returns them in marker order. A name that the
// steps no longer share stays in the steps, and the report says so.
func (c *plCtx) liftEnv(jobs []*ir.Job, path string, f caps.Feature, names []string) []ir.Var {
	var steps []*ir.Step
	for _, j := range jobs {
		for _, st := range j.Steps {
			if st.Kind == ir.StepScript {
				steps = append(steps, st)
			}
		}
	}
	var lifted []ir.Var
	for _, name := range names {
		v, ok := sameEnv(steps, name)
		if !ok {
			c.ignored(path, f, "the variable "+name+" is no longer the same in every step; it stays in the steps")
			continue
		}
		lifted = append(lifted, v)
		for _, st := range steps {
			st.Env = slices.DeleteFunc(st.Env, func(o ir.Var) bool { return o.Name == name })
		}
	}
	return lifted
}

// jobEnvMarker puts the variables that the emitter copied from the job back into Job.Env.
func (b *jobBuilder) jobEnvMarker() {
	var a envArg
	if markerArg(b.c.src.doc.Head, b.path, caps.JobEnv, &a) {
		b.job.Env = append(b.job.Env, b.c.liftEnv([]*ir.Job{b.job}, b.path, caps.JobEnv, a.Names)...)
	}
}

// pipelineEnvMarker puts the variables that the emitter copied from the pipeline back into Pipeline.Env.
func (c *plCtx) pipelineEnvMarker(pl *ir.Pipeline) {
	var a envArg
	path := "pipeline/" + c.plID
	if markerArg(c.src.doc.Head, path, caps.PipelineEnv, &a) {
		pl.Env = append(pl.Env, c.liftEnv(append(slices.Clone(pl.Jobs), pl.Finally...), path, caps.PipelineEnv, a.Names)...)
	}
}
