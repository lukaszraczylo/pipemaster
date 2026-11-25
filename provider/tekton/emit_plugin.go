package tekton

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const pluginStrategy = "plugin image with PLUGIN_ variables"

// pluginStep writes a plugin step as a step that runs the image entrypoint with the settings as PLUGIN_ variables.
// It reports false, writing nothing, when the step has a condition, a guard or a setting with no Tekton spelling:
// the caller then writes the failing step.
func (tc *taskCtx) pluginStep(b *stepBook, st *ir.Step, sp, name string) (*wmap, bool, error) {
	if !emitkit.PluginRuns(st) || b.guard != nil || tc.e.gated[st] {
		return nil, false, nil
	}
	vars, why := emitkit.PluginEnv(st)
	if why != "" {
		tc.e.lose(sp, caps.StepAction, why+"; the step fails with a message")
		return nil, false, nil
	}
	sc := newScope()
	env, err := tc.envList(sc, sp, slices.Concat(vars, st.Env))
	if err != nil {
		return nil, false, err
	}
	if len(env) != len(vars)+len(st.Env) {
		return nil, false, nil
	}
	img, ok, err := tc.text(sc, sp, caps.StepImage, valueText, st.Image.Ref)
	if err != nil || !ok {
		return nil, false, err
	}
	m := newMap()
	m.SetStr("name", name)
	if st.Name != "" {
		m.SetStr("displayName", st.Name)
	}
	m.SetStr("image", img)
	if len(env) > 0 {
		m.Set("env", seqOf(env...))
	}
	tc.stepTail(m, st, condition{})
	tc.e.emulate(sp, caps.StepAction, pluginStrategy, "the settings are passed as PLUGIN_<NAME> variables, the Woodpecker convention; a list of scalars is comma-joined, any other list or map is JSON; the image entrypoint runs and the step has no script")
	return m, true, tc.markAction(b, sp, st, "", m)
}
