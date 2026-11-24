package woodpecker

import (
	"reflect"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// carryYAML writes v in the IR's YAML form, which keeps conditions and values that JSON cannot hold.
func carryYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

// posArg points a marker at a step by its place in the file, because a made-up step ID differs in the parsed IR.
type posArg struct {
	Name string `json:"name,omitempty"`
	// Wrote is the name as written when it differs from Name, which keeps the source text.
	Wrote string   `json:"wrote,omitempty"`
	Refs  []string `json:"refs,omitempty"`
	// Broken lists the references that the conversion wrote inside a quote break.
	Broken []string `json:"broken,omitempty"`
	Pos    int      `json:"pos"`
}

// srcArg carries a condition in the dialect it came from, which the translated when entries only approximate.
type srcArg struct {
	Src  string `json:"src"`
	From string `json:"from"`
	// Branch is true when the translation used the trigger facts (expr.Facts), so the return trip must too.
	Branch bool `json:"branch,omitempty"`
}

// axesArg names matrix variables that a file reads and does not declare, because the matrix has no Woodpecker form.
type axesArg struct {
	Axes []string `json:"axes"`
}

// markStrayAxes writes the marker for the matrix names the workflow reads and its matrix does not declare.
func (e *emitter) markStrayAxes(c *wfCtx) error {
	var stray []string
	for _, n := range e.matrixRefs {
		if !declares(c.j.Matrix, n) {
			stray = append(stray, n)
		}
	}
	if len(stray) == 0 {
		return nil
	}
	return e.markArg(caps.ScriptInterpolation, c.jp, axesArg{Axes: stray})
}

func declares(mx *ir.Matrix, name string) bool {
	if mx == nil {
		return false
	}
	if slices.ContainsFunc(mx.Axes, func(a ir.Axis) bool { return a.Name == name }) {
		return true
	}
	return slices.ContainsFunc(mx.Include, func(c ir.Combo) bool {
		return slices.ContainsFunc(c, func(kv ir.KV) bool { return kv.Key == name })
	})
}

// shadowArg names the variables a step sets itself that a job or the pipeline sets to the same value, which the
// file cannot tell from the inherited one.
type shadowArg struct {
	Names []string `json:"names"`
	Pos   int      `json:"pos"`
}

// markShadows writes the marker for the variables of the step that repeat an inherited one.
func (e *emitter) markShadows(c *wfCtx, sp string, st *ir.Step) error {
	var names []string
	for _, v := range st.Env {
		for _, outer := range [][]ir.Var{c.j.Env, c.pl.Env} {
			if slices.ContainsFunc(outer, func(o ir.Var) bool { return o.Name == v.Name && reflect.DeepEqual(o.Value, v.Value) }) {
				names = append(names, v.Name)
				break
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return e.markArg(caps.StepEnv, sp, shadowArg{Names: names, Pos: c.pos})
}

// lostEnvArg carries the variables of a step whose value has no Woodpecker spelling, each with its place in the
// step's own list.
type lostEnvArg struct {
	Vars string `json:"vars"`
	At   []int  `json:"at"`
	Pos  int    `json:"pos"`
}

// markLostEnv writes the marker that holds the variables a step lost.
func (e *emitter) markLostEnv(path string, pos int, at []int, vs []ir.Var) error {
	body, err := carryYAML(vs)
	if err != nil {
		return err
	}
	return e.markArg(caps.ExprOpaque, path, lostEnvArg{Vars: body, At: at, Pos: pos})
}

// markVars writes a marker whose argument holds the variables copied into every step.
func (e *emitter) markVars(f caps.Feature, path string, vs []ir.Var) error {
	body, err := carryYAML(vs)
	if err != nil {
		return err
	}
	return e.markArg(f, path, body)
}

// finallyArg carries what the finally workflow lost: its own needs, which the all-workflows dependency replaces,
// and whether it had a condition, because the status gate reads back as one.
type finallyArg struct {
	Needs []ir.Need `json:"needs,omitempty"`
	NoIf  bool      `json:"noIf,omitempty"`
}

// markTriggers writes a marker whose argument holds the pipeline triggers as the IR had them, because a file
// holds them only as when entries that change a glob's meaning or replace the triggers of a manual workflow.
func (e *emitter) markTriggers(f caps.Feature, path string, ts []ir.Trigger) error {
	body, err := carryYAML(ts)
	if err != nil {
		return err
	}
	return e.markArg(f, path, body)
}
