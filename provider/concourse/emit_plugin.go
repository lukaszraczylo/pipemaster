package concourse

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	pluginStrategy = "plugin image as the task image, entrypoint command as run.path"
	pluginCaveat   = "the entrypoint command comes from pipemaster's plugin table (%s); the settings are PLUGIN_ variables in the task params, the Woodpecker convention"
	pluginPrivMsg  = "; the plugin starts a Docker daemon, so the task needs a privileged container, which the task file cannot request"
)

// pluginTask writes a plugin step as a task that runs the entrypoint command of the plugin image. It returns nil,
// writing nothing, when the plugin is not in the table, the job has a guard or a setting has no spelling: the
// caller then writes the failing task.
func (c *chain) pluginTask(st *ir.Step, path string) []*yaml.Node {
	jx := c.jx
	e := jx.pe.e
	if st.If != nil || !emitkit.PluginRuns(st) || jx.guard != nil || strings.Contains(st.Image.Ref, varOpen) {
		return nil
	}
	pl, ok := actions.LookupPlugin(st.Image.Ref)
	if !ok {
		return nil
	}
	vars, why := emitkit.PluginEnv(st)
	if why != "" {
		e.lose(path, caps.StepAction, why+"; the step fails with a message")
		return nil
	}
	name := jx.stepName(st, path)
	dir := jx.runDir(st, path, name, nil)
	sc := jx.newScope(dir)
	all := slices.Concat(vars, st.Env)
	sc.envParams(path, all)
	if len(sc.params) != len(all) {
		return nil
	}
	repo, tag, digest := splitImage(st.Image.Ref)
	cfg := newMap()
	cfg.SetStr("platform", defaultPlatform)
	cfg.Set("image_resource", imageNode(repo, digest, tag))
	ws := func() *yaml.Node {
		m := newMap()
		m.SetStr("name", workspaceName)
		return seqOf(m.N)
	}
	cfg.Set("inputs", ws())
	cfg.Set("outputs", ws())
	params := newMap()
	for _, p := range sc.params {
		params.SetStr(p.name, p.value)
	}
	if !params.Empty() {
		cfg.Set("params", params.N)
	}
	run := newMap()
	run.SetStr("path", pl.Entrypoint[0])
	if len(pl.Entrypoint) > 1 {
		run.Set("args", strSeq(pl.Entrypoint[1:]))
	}
	if dir != "" {
		run.SetStr("dir", dir)
	}
	cfg.Set("run", run.N)
	m := newMap()
	m.SetStr("task", name)
	m.Set("config", cfg.N)
	if st.Timeout != nil {
		m.SetStr("timeout", durationText(st.Timeout))
	}
	jx.secretMarker(sc, name)
	held := *st
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		e.lose(path, caps.StepAction, "the action marker cannot be written: "+err.Error())
		return nil
	}
	arg := actionArg{Step: body, Task: name, Hold: nodeCanon(m.N)}
	e.markArg(caps.StepAction, jx.sm(name), arg)
	caveat := strings.Replace(pluginCaveat, "%s", pl.Source, 1)
	if pl.Privileged {
		caveat += pluginPrivMsg
	}
	e.emulate(path, caps.StepAction, pluginStrategy, caveat)
	return []*yaml.Node{m.N}
}
