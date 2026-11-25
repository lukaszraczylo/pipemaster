package gitlab

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	pluginStrategy = "plugin image as the job image, entrypoint command as the script"
	pluginCaveat   = "the entrypoint command comes from pipemaster's plugin table (%s); the settings are PLUGIN_ variables, the Woodpecker convention"
	privStrategy   = "privileged runner"
	privDetail     = "the plugin starts a Docker daemon: set privileged = true in the [runners.docker] section of the runner config.toml, because a GitLab file cannot request it"
)

// pluginCommand is the shell line that runs the entrypoint of a plugin image.
func pluginCommand(argv []string) string {
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = singleQuote(a)
	}
	return strings.Join(words, " ")
}

// pluginWord spells a value as one shell word. Literal text is single-quoted, so no byte of it runs or expands; a
// variable reference expands. It returns why when the value has no safe spelling.
func (e *emitter) pluginWord(sp string, v ir.Value) (word, why string) {
	switch v.Kind {
	case ir.ValueSecret:
		if !isIdent(v.Secret) {
			return "", "the secret " + v.Secret + " is not a GitLab variable name"
		}
		e.secretSetup(sp, caps.StepEnv, v.Secret)
		return `"$` + v.Secret + `"`, ""
	case ir.ValueExpr:
		w, ok := shellWord(v.Expr)
		if !ok {
			return "", "an expression has no GitLab variable"
		}
		return w, ""
	}
	parts, err := expr.ParseScriptTemplate(v.Lit)
	if err != nil {
		return "", "the value cannot be read: " + err.Error()
	}
	var sb strings.Builder
	for _, p := range parts {
		text := p.Text
		if p.Expr != nil {
			w, ok := shellWord(p.Expr)
			if !ok {
				return "", "an expression has no GitLab variable"
			}
			sb.WriteString(w)
			continue
		}
		if strings.ContainsAny(text, "\r\x00") {
			return "", "the value holds a carriage return or NUL byte, which a GitLab script cannot carry"
		}
		sb.WriteString(sq(text))
	}
	if sb.Len() == 0 {
		return "''", ""
	}
	return sb.String(), ""
}

// pluginScript writes the subshell that sets the settings and runs the entrypoint; why says what has no spelling.
func (e *emitter) pluginScript(sp string, vars []ir.Var, argv []string) (string, string) {
	lines := []string{"("}
	for _, v := range vars {
		if !isIdent(v.Name) {
			return "", "the variable " + v.Name + " is not a shell identifier"
		}
		w, why := e.pluginWord(sp, v.Value)
		if why != "" {
			return "", "the setting " + v.Name + ": " + why
		}
		lines = append(lines, "export "+v.Name+"="+w)
	}
	lines = append(lines, pluginCommand(argv), ")")
	return strings.Join(lines, "\n"), ""
}

// lowerPlugins returns j with each plugin step that has a known entrypoint written as a script step: the plugin
// image with an empty entrypoint and a script that sets the settings and runs the entrypoint. A step that cannot
// run this way keeps its failing form, and the reason goes to the report text. It returns j itself when no step
// changes.
func (e *emitter) lowerPlugins(plID string, j *ir.Job) *ir.Job {
	var out *ir.Job
	for i, st := range j.Steps {
		if st == nil || st.If != nil || !emitkit.PluginRuns(st) || e.gated[st] {
			continue
		}
		pl, ok := actions.LookupPlugin(st.Image.Ref)
		if !ok {
			continue
		}
		key := pluginKey(j.ID, st.ID)
		sp := stepPath(jobPath(plID, j.ID), st.ID)
		if !pl.HasShell() {
			e.pluginWhy[key] = "the plugin image " + st.Image.Ref + " is built from scratch and holds no shell, and a GitLab job runs its script in a shell; run the image outside GitLab or build an image that adds a shell"
			continue
		}
		vars, why := emitkit.PluginEnv(st)
		if why == "" {
			var script string
			script, why = e.pluginScript(sp, slices.Concat(vars, st.Env), pl.Entrypoint)
			if why == "" {
				c := *st
				c.Kind, c.With, c.Env = ir.StepScript, nil, nil
				c.Image = &ir.Image{Ref: st.Image.Ref, Entrypoint: []string{""}}
				c.Script = []string{script}
				if out == nil {
					cp := *j
					cp.Steps = slices.Clone(j.Steps)
					out = &cp
				}
				out.Steps[i] = &c
				e.plugins[key] = pluginUse{orig: st, plugin: pl}
				continue
			}
		}
		e.pluginWhy[key] = "the plugin step has a setting with no safe GitLab spelling (" + why + "); the step fails with a message"
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
	e.emulate(sp, caps.StepAction, pluginStrategy, strings.Replace(pluginCaveat, "%s", u.plugin.Source, 1))
	if u.plugin.Privileged {
		e.external(sp, caps.JobRunner, privStrategy, privDetail)
	}
	held := *u.orig
	held.Ext = nil
	return e.markActionStep(p, sp, &held, actionStepArg{Kind: actScript, Script: strings.Join(st.Script, "\n")})
}

// dropPlugins forgets the plugin steps of a job that stayed whole, so they keep their failing form.
func (e *emitter) dropPlugins(jobID, why string) {
	for k := range e.plugins {
		if strings.HasPrefix(k, jobID+"/") {
			delete(e.plugins, k)
			if why != "" {
				e.pluginWhy[k] = "a plugin image needs its own job and the job is not split because " + why
			}
		}
	}
}
