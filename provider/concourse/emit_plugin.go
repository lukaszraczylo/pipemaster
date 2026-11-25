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
	pluginPrivMsg  = "; the plugin starts a Docker daemon, so the task step sets privileged: true, and the workers must allow privileged tasks"
)

// pluginTask writes a plugin step as a task that runs the entrypoint command of the plugin image. It returns nil,
// writing nothing, when the plugin is not in the table, the job has a guard or a setting has no spelling: the
// caller then writes the failing task.
func (c *chain) pluginTask(st *ir.Step, path string) ([]*yaml.Node, string) {
	jx := c.jx
	e := jx.pe.e
	if st.Image == nil {
		return nil, ""
	}
	pl, ok := actions.LookupPlugin(st.Image.Ref)
	switch {
	case !ok || !emitkit.PluginRuns(st):
		return nil, ""
	case st.If != nil:
		return nil, "the step has a condition that a plugin task cannot carry; the step fails with a message"
	case jx.guard != nil:
		return nil, "the job has a guard that a plugin task cannot carry; the step fails with a message"
	case strings.Contains(st.Image.Ref, varOpen):
		return nil, "the image reference holds ((, which Concourse reads as a variable; the step fails with a message"
	}
	vars, why := emitkit.PluginEnv(st)
	if why != "" {
		return nil, why + "; the step fails with a message"
	}
	name := jx.stepName(st, path)
	dir := jx.runDir(st, path, name, nil)
	sc := jx.newScope(dir)
	all := slices.Concat(vars, st.Env)
	sc.envParams(path, all)
	if len(sc.params) != len(all) {
		return nil, "a setting has no Concourse param spelling; the step fails with a message"
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
	if !c.mapped && jx.workspaceSource().name != workspaceName {
		im := newMap()
		im.SetStr(workspaceName, jx.ws.name)
		m.Set("input_mapping", im.N)
		jx.noteWorkspace()
	}
	c.mapped = true
	if pl.Privileged {
		m.Set("privileged", boolNode(true))
	}
	if st.Timeout != nil {
		m.SetStr("timeout", durationText(st.Timeout))
	}
	jx.secretMarker(sc, name)
	held := *st
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		return nil, "the action marker cannot be written: " + err.Error()
	}
	arg := actionArg{Step: body, Task: name, Hold: nodeCanon(m.N)}
	e.markArg(caps.StepAction, jx.sm(name), arg)
	caveat := strings.Replace(pluginCaveat, "%s", pl.Source, 1)
	if pl.Privileged {
		caveat += pluginPrivMsg
	}
	e.emulate(path, caps.StepAction, pluginStrategy, caveat)
	return []*yaml.Node{m.N}, ""
}

// bridgeArg is the marker argument of the failing task that stands for a job with no steps.
type bridgeArg struct {
	Task string `json:"task"`
}

const bridgeTaskName = "bridge"

// bridgeTask writes the failing task of a job whose work lives in another pipeline or in an include, so the job
// does not pass with only a get. It returns nil for a job that holds no such work.
func (jx *jobEmitter) bridgeTask() []*yaml.Node {
	e := jx.pe.e
	if len(jx.j.Steps) > 0 {
		return nil
	}
	reason, ok := emitkit.BridgeWork(jx.j)
	if !ok {
		return nil
	}
	name := unique(jx.taken, bridgeTaskName)
	cfg := newMap()
	cfg.SetStr("platform", defaultPlatform)
	cfg.Set("image_resource", imageNode(defaultImage, "", defaultTag))
	run := newMap()
	run.SetStr("path", shellCommand)
	run.Set("args", strSeq([]string{defaultFlags, emitkit.FailLoud(caps.StepScript, jx.path, reason)}))
	cfg.Set("run", run.N)
	m := newMap()
	m.SetStr("task", name)
	m.Set("config", cfg.N)
	e.lose(jx.path+"/step/script", caps.StepScript, reason+"; a task that prints the loss and exits 1 keeps the job honest")
	e.emulate(jx.path, caps.StepScript, "failing task", reason+"; a task that prints the loss and exits 1 stands in")
	e.markArg(caps.StepScript, jx.mpath, bridgeArg{Task: name})
	return []*yaml.Node{m.N}
}
