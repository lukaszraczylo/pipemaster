package argo

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const pluginStrategy = "plugin image with PLUGIN_ variables"

// pluginContainer writes a plugin step as a container that runs the image entrypoint with the settings as PLUGIN_
// variables. It returns nil, writing nothing, when the step writes a job output, has a condition or has a setting
// with no Argo spelling: the caller then writes the failing container.
func (sx *stepCtx) pluginContainer(st *ir.Step, name string, last bool) *container {
	tc, e := sx.tc, sx.tc.e
	path := tc.jp.path + "/step/" + st.ID
	if last || st.If != nil || !emitkit.PluginRuns(st) || e.gated[st] {
		return nil
	}
	vars, why := emitkit.PluginEnv(st)
	if why != "" {
		e.lose(path, caps.StepAction, why+"; the step fails with a message")
		return nil
	}
	all := slices.Concat(vars, st.Env)
	if name, ok := holdsTag(all); ok {
		e.lose(path, caps.StepAction, "the setting "+name+" holds {{ or }}, which Argo reads as a tag and has no documented escape for; the step fails with a message")
		return nil
	}
	sc := newScope()
	env := tc.envList(sc, path, all)
	if len(env) != len(all) {
		return nil
	}
	if name == "" {
		name = e.dnsName(sx.nameTaken, path, "step id", st.ID, maxName, true)
	}
	c := &container{st: st, name: name, m: newMap()}
	c.m.SetStr("name", name)
	if name != st.ID {
		c.m.KeyComment("name", idComment(st.ID))
	}
	e.markName(path, st.Name)
	c.image = sx.imageOf(st, path)
	c.m.SetStr("image", c.image)
	c.m.SetStr("workingDir", workspaceMount)
	if len(env) > 0 {
		c.m.Set("env", seqOf(env...))
	}
	priv := ""
	if pl, ok := actions.LookupPlugin(st.Image.Ref); ok && pl.Privileged {
		sec := newMap()
		sec.Set("privileged", boolNode(true))
		c.m.Set("securityContext", sec.N)
		priv = "; the plugin starts a Docker daemon, so it sets securityContext.privileged: true and the cluster policy must admit privileged pods"
	}
	sx.act.Hold = nodeCanon(c.m.N)
	e.emulate(path, caps.StepAction, pluginStrategy, "the settings are passed as PLUGIN_<NAME> variables, the Woodpecker convention; a list of scalars is comma-joined, any other list or map is JSON; the image entrypoint runs and the container has no command"+priv)
	return c
}

// holdsTag reports the first variable whose literal text holds a tag mark that Argo would substitute.
func holdsTag(vars []ir.Var) (string, bool) {
	for _, v := range vars {
		if v.Value.Kind != ir.ValueLit {
			continue
		}
		parts, err := expr.ParseScriptTemplate(v.Value.Lit)
		if err != nil {
			return v.Name, true
		}
		for _, p := range parts {
			if p.Expr == nil && (strings.Contains(p.Text, "{{") || strings.Contains(p.Text, "}}")) {
				return v.Name, true
			}
		}
	}
	return "", false
}
