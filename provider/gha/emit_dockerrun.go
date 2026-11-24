package gha

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	runImageVar  = "PIPEMASTER_IMAGE"
	runScriptVar = "PIPEMASTER_SCRIPT"
	runStrategy  = "docker run in a script step"
	runCaveat    = "the step needs a runner with Docker; the container runs as the image user, usually root, so files it writes in the workspace can belong to another user than the runner user, and it sees only the workspace and the variables the step passes"
)

var (
	envNameRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	exprTextRE = regexp.MustCompile(`\$\{\{.*?\}\}`)
)

// dockerRunLine is the shell line that runs a step script in its image. Only variable names, set by the step
// env, reach the line: the image and the script travel in environment variables, so no source text is parsed by the host shell.
func dockerRunLine(passed []string, inWorkDir bool) string {
	dir := `-w "$GITHUB_WORKSPACE"`
	if inWorkDir {
		dir = `-w "$PWD"`
	}
	parts := []string{"docker run --rm", `-v "$GITHUB_WORKSPACE:$GITHUB_WORKSPACE"`, dir}
	for _, n := range passed {
		parts = append(parts, "-e "+n)
	}
	parts = append(parts, "--entrypoint "+defaultEntrypoint, `"$`+runImageVar+`"`, `-ec "$`+runScriptVar+`"`)
	return strings.Join(parts, " ")
}

// passedEnv lists the variables the step passes into the container: the workflow, job and step variables.
func (e *emitter) passedEnv(st *ir.Step) []string {
	var out []string
	for _, n := range append(slices.Clone(e.passEnv), varNames(st.Env)...) {
		if envNameRE.MatchString(n) && !strings.HasPrefix(n, "PIPEMASTER_") && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func varNames(vs []ir.Var) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Name
	}
	return out
}

// dockerRunStep writes a script step whose image reference holds an expression as a docker run line. It reports
// false, writing nothing, when the reference or the entrypoint has no safe spelling.
func (e *emitter) dockerRunStep(m *omap, sp string, st *ir.Step) (bool, error) {
	if st.Kind != ir.StepScript || len(st.Script) == 0 {
		return false, nil
	}
	if ep := st.Image.Entrypoint; len(ep) > 0 && ep[0] != "" {
		return false, nil
	}
	ref, err := e.template(sp, st.Image.Ref)
	if err != nil || strings.Contains(exprTextRE.ReplaceAllString(ref, ""), "$") {
		return false, nil
	}
	script, err := e.templates(sp, e.withoutRemoteRefs(sp, st.Script))
	if err != nil {
		return false, err
	}
	passed := e.passedEnv(st)
	m.set("run", str(dockerRunLine(passed, st.WorkDir != "")))
	vars := newMap()
	vars.set(runImageVar, str(ref))
	vars.set(runScriptVar, block(script))
	e.pluginVars = vars.n
	e.emulate(sp, caps.StepImage, runStrategy, runCaveat)
	return true, e.markArg(caps.StepImage, sp, imageArg{Image: *st.Image, Script: st.Script, Kind: st.Kind, WorkDir: st.WorkDir, Run: true, Passed: passed})
}

// collapseDockerRun turns a docker run step back into the image step it was written from, while the parsed
// step still reads as written.
func collapseDockerRun(e *emitter, st *ir.Step, u ir.FeatureUse, a imageArg, rep *report.Report) {
	ref, rerr := e.template(u.IRPath, a.Image.Ref)
	script, serr := e.templates(u.IRPath, a.Script)
	image := slices.IndexFunc(st.Env, func(v ir.Var) bool { return v.Name == runImageVar })
	body := slices.IndexFunc(st.Env, func(v ir.Var) bool { return v.Name == runScriptVar })
	if rerr != nil || serr != nil || image < 0 || body < 0 || st.Action != nil ||
		!slices.Equal(st.Script, []string{dockerRunLine(a.Passed, a.WorkDir != "")}) ||
		valueText(st.Env[image].Value) != ref ||
		strings.TrimRight(valueText(st.Env[body].Value), "\n") != strings.TrimRight(strings.Join(script, "\n"), "\n") {
		rep.Add(report.Entry{IRPath: u.IRPath, Feature: u.Feature, Level: caps.Lost, Strategy: "marker ignored",
			Detail: "marker ignored: the docker run step was edited after conversion, so the parsed step stands"})
		return
	}
	st.Env = slices.DeleteFunc(st.Env, func(v ir.Var) bool { return v.Name == runImageVar || v.Name == runScriptVar })
	if len(st.Env) == 0 {
		st.Env = nil
	}
	st.Kind, st.Image, st.Script = a.Kind, &a.Image, a.Script
	if st.WorkDir == "" {
		st.WorkDir = a.WorkDir
	}
}
