package concourse

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// heldGet writes a checkout step that a Concourse file held as an in_parallel step of gets. The step stays as the
// file had it, so the get of the repository keeps its trigger and its passed list.
func (c *chain) heldGet(st *ir.Step, path string) ([]*yaml.Node, bool) {
	e := c.jx.pe.e
	ext := extOf(st.Ext)
	if !e.own || ext[ExtStep] == nil {
		return nil, false
	}
	if d, _ := ext[ExtTaskDigest].(string); d != taskDigest(st, "") {
		e.lose(path, caps.StepAction, "the checkout step was edited, and the gets of the plan hold it; the step stays as the file had it")
	}
	n := c.verbatim(st, path)
	if n == nil {
		return nil, true
	}
	return []*yaml.Node{n}, true
}

// taskFile writes a task that a task file gave as the reference to the file, while the step still has what the
// parser read from it. An edited step is written as an inline config, and the report says that the file reference
// is gone.
func (c *chain) taskFile(st *ir.Step, path string) *yaml.Node {
	jx := c.jx
	e := jx.pe.e
	ext := extOf(st.Ext)
	file := strOf(ext["file"])
	if !e.own || file == "" {
		return nil
	}
	image := refOf(st.Image)
	if image == "" {
		image = refOf(jx.j.Container)
	}
	if d, _ := ext[ExtTaskDigest].(string); d != taskDigest(st, image) || st.If != nil || jx.guard != nil || len(jx.j.Env) > 0 || len(jx.pe.pl.Env) > 0 {
		e.emulate(path, caps.StepScript, "inline task config", "the step no longer matches the task file "+file+", so the task holds the config itself")
		return nil
	}
	n := c.verbatim(st, path)
	if n == nil || !st.ContinueOnError {
		return n
	}
	e.emulate(path, caps.StepContinueOnError, "try around the step", "a failure of the step does not fail the job")
	try := newMap()
	try.Set("try", n)
	return try.N
}

// fileDefaultsOf maps the params that the task file defaults to their text.
func fileDefaultsOf(ext map[string]any) map[string]string {
	out := map[string]string{}
	l, _ := ext[ExtFileParams].([]any)
	for _, raw := range l {
		if m, ok := raw.(map[string]any); ok {
			out[strOf(m["name"])] = strOf(m["value"])
		}
	}
	return out
}

// taskMarker writes what the parser read from a task file, so a parser that cannot read the file restores the step.
func (jx *jobEmitter) taskMarker(st *ir.Step, name, file string, ext map[string]any) {
	a := taskArg{File: file, Shell: st.Shell, WorkDir: st.WorkDir, Script: st.Script, Image: refOf(st.Image)}
	if a.Image == "" {
		a.Image = refOf(jx.j.Container)
	}
	l, _ := ext[ExtFileParams].([]any)
	for _, raw := range l {
		m, _ := raw.(map[string]any)
		a.Params = append(a.Params, [2]string{strOf(m["name"]), strOf(m["value"])})
	}
	jx.pe.e.markArg(caps.StepScript, jx.sm(name), a)
}
