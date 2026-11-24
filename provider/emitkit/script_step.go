package emitkit

import (
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// ScriptStep is the command step that stands for an action step whose inputs a script carries. Each variable
// that the script reads takes the value of the step input, so the value goes through the usual expression and
// variable translation and never into the script text. needsImage asks for the image the registry names, for
// a step that has none of its own and finds none on the job.
func ScriptStep(st *ir.Step, t actions.Target, path string, needsImage bool) *ir.Step {
	cp := *st
	cp.Script, cp.Kind, cp.Action, cp.With = t.Script, ir.StepScript, nil, nil
	cp.Env = append([]ir.Var(nil), st.Env...)
	names := make([]string, 0, len(t.Env)+len(t.EnvLit))
	for v := range t.Env {
		names = append(names, v)
	}
	for v := range t.EnvLit {
		names = append(names, v)
	}
	sort.Strings(names)
	var guard []string
	for _, v := range names {
		if in, ok := t.Env[v]; ok {
			cp.Env = append(cp.Env, ir.Var{Name: v, Value: st.With[in]})
			// A target that cannot spell the value leaves the variable unset; the step then stops instead of running without the input.
			guard = append(guard, ": \"${"+v+"?pipemaster: lost: "+failLoudText("step.action "+path+": input "+in+" has no value on this target")+"}\"")
			continue
		}
		cp.Env = append(cp.Env, ir.Var{Name: v, Value: ir.Value{Kind: ir.ValueLit, Lit: t.EnvLit[v]}})
	}
	cp.Script = []string{strings.Join(append(guard, t.Script...), "\n")}
	if needsImage && cp.Image == nil && t.Image != "" {
		cp.Image = &ir.Image{Ref: t.Image}
	}
	return &cp
}
