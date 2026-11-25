package gitlab

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	pluginStrategy = "plugin image as the job image, entrypoint command as the script"
	pluginCaveat   = "the entrypoint command comes from pipemaster's plugin table (%s); the settings are PLUGIN_ variables, the Woodpecker convention"
	pluginPrivMsg  = "; the plugin starts a Docker daemon, so the runner must allow privileged containers"
)

// pluginCommand is the shell line that runs the entrypoint of a plugin image.
func pluginCommand(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = singleQuote(a)
	}
	return strings.Join(words, " ")
}

// lowerPlugins returns j with each plugin step that has a known entrypoint written as a script step: the plugin
// image with an empty entrypoint, the entrypoint command as the script and the settings as variables. It returns
// j itself when no step changes.
func (e *emitter) lowerPlugins(j *ir.Job) *ir.Job {
	var out *ir.Job
	for i, st := range j.Steps {
		if st == nil || st.If != nil || !emitkit.PluginRuns(st) || e.gated[st] {
			continue
		}
		pl, ok := actions.LookupPlugin(st.Image.Ref)
		if !ok {
			continue
		}
		vars, why := emitkit.PluginEnv(st)
		if why != "" {
			continue
		}
		c := *st
		c.Kind, c.With = ir.StepScript, nil
		c.Image = &ir.Image{Ref: st.Image.Ref, Entrypoint: []string{""}}
		c.Script = []string{pluginCommand(pl.Entrypoint)}
		c.Env = slices.Concat(vars, st.Env)
		if out == nil {
			cp := *j
			cp.Steps = slices.Clone(j.Steps)
			out = &cp
		}
		out.Steps[i] = &c
		e.plugins[pluginKey(j.ID, st.ID)] = pluginUse{orig: st, plugin: pl}
	}
	if out == nil {
		return j
	}
	return out
}

// pluginUse is a plugin step that the emitter wrote as a script step.
type pluginUse struct {
	orig   *ir.Step
	plugin actions.Plugin
}

func pluginKey(jobID, stepID string) string { return jobID + "/" + stepID }

// pluginMark reports and marks a script step that stands for a plugin step; it does nothing for other steps.
func (e *emitter) pluginMark(p *jobPrep, st *ir.Step) error {
	jobID := p.j.ID
	if s, ok := e.splits[p.j]; ok {
		jobID = s.arg.Job
	}
	u, ok := e.plugins[pluginKey(jobID, st.ID)]
	if !ok {
		return nil
	}
	sp := stepPath(p.path, st.ID)
	caveat := strings.Replace(pluginCaveat, "%s", u.plugin.Source, 1)
	if u.plugin.Privileged {
		caveat += pluginPrivMsg
	}
	e.emulate(sp, caps.StepAction, pluginStrategy, caveat)
	held := *u.orig
	held.Ext = nil
	return e.markActionStep(p, sp, &held, actionStepArg{Kind: actScript, Script: strings.Join(st.Script, "\n")})
}

// dropPlugins forgets the plugin steps of a job that stayed whole, so they keep their failing form.
func (e *emitter) dropPlugins(jobID string) {
	for k := range e.plugins {
		if strings.HasPrefix(k, jobID+"/") {
			delete(e.plugins, k)
		}
	}
}
