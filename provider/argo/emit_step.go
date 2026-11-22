package argo

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprargo "github.com/lukaszraczylo/pipemaster/expr/argo"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	workspaceName   = "workspace"
	workspaceMount  = "/workspace"
	outputsDir      = workspaceMount + "/.pipemaster/outputs"
	artifactsDir    = workspaceMount + "/artifacts"
	shellCommand    = "sh"
	shellFlags      = "-ec"
	continueTrap    = "trap 'exit 0' EXIT # pipemaster:continue-on-error"
	disabledScript  = "exit 0 # pipemaster:lost the step condition has no shell form"
	cloneHost       = "https://github.com/"
	gitArtifactName = "source"
	outEnvPrefix    = "PM_OUT_"
)

var nonIdent = regexp.MustCompile(`[^A-Za-z0-9_]`)

// container is one container of a containerSet under construction.
type container struct {
	st    *ir.Step
	m     *wmap
	name  string
	image string
	// own is true when the step holds the command and args of an Argo file that it still matches.
	own bool
}

// stepCtx carries the state of the steps of one job.
type stepCtx struct {
	tc      *jobCtx
	taken   map[string]bool
	image   string
	artIn   []*yaml.Node
	artOut  []*yaml.Node
	argArts []*yaml.Node
}

// stepName gives each step with a container its name. The last container is main when the job has outputs or
// artifacts, because Argo reads them from the container of that name only.
func stepNames(steps []*ir.Step, wantMain bool, e *emitter, path string) map[*ir.Step]string {
	names := map[*ir.Step]string{}
	taken := map[string]bool{}
	var last *ir.Step
	if wantMain && len(steps) > 0 {
		last = steps[len(steps)-1]
		names[last] = mainStep
		taken[mainStep] = true
	}
	for _, st := range steps {
		if st == last {
			continue
		}
		names[st] = e.dnsName(taken, path+"/step/"+st.ID, "step id", st.ID, maxName, true)
	}
	return names
}

// stepScripts converts the script lines of a step. A statement with no spelling is commented out whole.
func (tc *jobCtx) stepScripts(sc *stepScope, path string, st *ir.Step) []string {
	lines := make([]string, len(st.Script))
	why := make([]string, len(st.Script))
	for i, l := range st.Script {
		out, w, err := tc.scriptLine(sc, path, l)
		if err != nil {
			why[i] = "the text cannot be read: " + err.Error()
			continue
		}
		lines[i], why[i] = out, w
	}
	keep := emitkit.DropLost(st.Script, why, func(detail, text string) {
		tc.e.lose(path, caps.StepScript, detail)
		tc.e.comment(path, "script "+path+": "+text)
	})
	var out []string
	for _, i := range keep {
		out = append(out, lines[i])
	}
	return out
}

// containerFor writes one script step as a container.
func (sx *stepCtx) containerFor(st *ir.Step, name string, last bool, jx map[string]any) *container {
	tc, e := sx.tc, sx.tc.e
	path := tc.jp.path + "/step/" + st.ID
	c := &container{st: st, name: name, m: newMap()}
	sc := newScope()
	sx0 := extOf(st.Ext)
	if !e.own {
		sx0 = nil
	}
	c.m.SetStr("name", name)
	if name != st.ID {
		c.m.KeyComment("name", idComment(st.ID))
	}
	c.image = sx.imageOf(st, path)
	c.m.SetStr("image", c.image)
	script := tc.stepScripts(sc, path, st)
	script = sx.guarded(sc, path, st, script)
	if st.ContinueOnError {
		script = append([]string{continueTrap}, script...)
		e.emulate(path, caps.StepContinueOnError, "trap on exit", "the script ends with exit status 0, so a failure does not stop the containers that depend on it")
	}
	if last {
		script = append(script, tc.outputWriters(sc, path)...)
	}
	if line, ok := sx0[ExtScriptLine].(string); ok && len(st.Script) == 1 && st.Script[0] == line && !st.ContinueOnError && st.If == nil && !strings.Contains(line, "${{") {
		c.own = true
		for _, k := range []string{ExtCommand, ExtArgs} {
			if v := sx0[k]; v != nil {
				c.m.Set(strings.ToLower(k), e.node(path, k, v))
			}
		}
	} else {
		c.m.Set("command", strSeq([]string{shellCommand, shellFlags}))
		c.m.Set("args", seqOf(block(strings.Join(script, "\n"))))
	}
	workdir := workspaceMount
	if st.WorkDir != "" {
		workdir = tc.workdir(sc, path, st.WorkDir)
	}
	c.m.SetStr("workingDir", workdir)
	env := tc.envList(sc, path, st.Env)
	if merged := tc.mergeEnv(sc, env, sx0); len(merged) > 0 {
		c.m.Set("env", seqOf(merged...))
	}
	if sx0 != nil {
		if err := c.m.AddExtra(sx0, containerSkip); err != nil {
			e.lose(path, caps.ExtPassthrough, "the Ext of the step cannot be written: "+err.Error())
		}
	}
	_ = jx
	return c
}

// containerSkip lists the step Ext keys that the emitter writes itself.
var containerSkip = map[string]bool{ExtScriptLine: true, ExtCommand: true, ExtArgs: true, "valueFrom": true, keyDeps: true, ExtTemplate: true}

// workdir moves a relative working directory under the workspace.
func (tc *jobCtx) workdir(sc *stepScope, path, w string) string {
	parts, err := expr.ParseScriptTemplate(w)
	if err != nil {
		parts = []expr.Part{{Text: w}}
	}
	out, lost, hazards := tc.convert(sc, valueText, parts)
	if len(lost)+len(hazards) > 0 {
		tc.e.lose(path, caps.StepWorkdir, "the working directory has no Argo spelling; the step runs in "+workspaceMount)
		return workspaceMount
	}
	if !strings.HasPrefix(out, "/") {
		tc.e.emulate(path, caps.StepWorkdir, "under the workspace", "the relative working directory "+out+" is written under "+workspaceMount)
		return workspaceMount + "/" + out
	}
	return out
}

// guarded adds the shell guard for a step condition. A condition with no guard form disables the step.
func (sx *stepCtx) guarded(sc *stepScope, path string, st *ir.Step, script []string) []string {
	if st.If == nil {
		return script
	}
	tc := sx.tc
	if !exprargo.Guardable(st.If) {
		tc.e.lose(path, caps.StepIf, "the condition has no shell test form; the step is disabled")
		return []string{disabledScript}
	}
	env, ok := exprargo.GuardEnv(st.If)
	if !ok {
		tc.e.lose(path, caps.StepIf, "a reference of the condition has no container env spelling; the step is disabled")
		return []string{disabledScript}
	}
	// A reference to the output of another job needs the template input that carries it.
	for _, v := range env {
		sc.add(v.Name, nameValue(v.Name, tc.guardValue(v)))
	}
	out, err := exprargo.AddGuard(script, st.If)
	if err != nil {
		tc.e.lose(path, caps.StepIf, "the condition cannot be written as a guard: "+err.Error()+"; the step is disabled")
		return []string{disabledScript}
	}
	return out
}

// guardValue is the env value that the guard reads: the tag the vocabulary gives, with needs outputs bound to a template input.
func (tc *jobCtx) guardValue(v exprargo.EnvVar) string {
	if ref, ok := exprargo.EnvRef(v.Name); ok {
		if len(ref.Path) == 4 && ref.Path[0] == expr.PrefixNeeds {
			if n, ok := tc.needParam(ref.Path[1], ref.Path[3]); ok {
				return "{{inputs.parameters." + n + "}}"
			}
		}
		if p, ok := ctxParamOf(ref); ok {
			tc.ctxParam(p)
		}
	}
	return v.Value
}

func ctxParamOf(r expr.Ref) (string, bool) { return ctxparam.Param(r.Path) }

// imageOf picks the image of a step: its own, the one a setup action chose, the job image, or the default.
func (sx *stepCtx) imageOf(st *ir.Step, path string) string {
	tc := sx.tc
	for _, ref := range []string{imageRef(st.Image), sx.image, imageRef(tc.jp.j.Container)} {
		if ref == "" {
			continue
		}
		parts, err := expr.ParseScriptTemplate(ref)
		if err != nil {
			continue
		}
		sc := newScope()
		out, lost, hazards := tc.convert(sc, valueText, parts)
		if len(lost)+len(hazards) == 0 {
			return out
		}
		tc.e.lose(path, caps.StepImage, "the image "+ref+" has no Argo spelling; the default image "+defaultImage+" runs")
	}
	return defaultImage
}

func imageRef(i *ir.Image) string {
	if i == nil {
		return ""
	}
	return i.Ref
}

// mergeEnv merges the pipeline env, the job env, the scope env and the step env; a later entry wins.
func (tc *jobCtx) mergeEnv(sc *stepScope, step []*yaml.Node, ext map[string]any) []*yaml.Node {
	var all []*yaml.Node
	path := tc.jp.path
	all = append(all, tc.envList(newScope(), path, tc.pp.pl.Env)...)
	all = append(all, tc.envList(newScope(), path, tc.jp.j.Env)...)
	all = append(all, sc.env...)
	all = append(all, step...)
	if vf, ok := ext["valueFrom"].(map[string]any); ok {
		for _, name := range sortedKeys(vf) {
			m := newMap()
			m.SetStr("name", name)
			m.Set("valueFrom", tc.e.node(path, "valueFrom", vf[name]))
			all = append(all, m.N)
		}
	}
	last := map[string]int{}
	for i, n := range all {
		last[n.Content[1].Value] = i
	}
	var out []*yaml.Node
	for i, n := range all {
		if last[n.Content[1].Value] == i {
			out = append(out, n)
		}
	}
	return out
}

// outputWriters writes the lines that put the value of each job output into its file. An output with no value
// is a file that the script writes.
func (tc *jobCtx) outputWriters(sc *stepScope, path string) []string {
	var lines []string
	for _, o := range tc.jp.j.Outputs {
		if o.Value == "" {
			continue
		}
		env := outputEnv(o.Name)
		n := tc.valueNode(sc, path, env, ir.Value{Lit: o.Value})
		if n == nil {
			continue
		}
		sc.add(env, n)
		lines = append(lines, outputEpilogue(o.Name))
	}
	return lines
}

// outputEnv names the env variable that carries the value of an output.
func outputEnv(name string) string {
	return outEnvPrefix + strings.ToUpper(nonIdent.ReplaceAllString(name, "_"))
}

// outputFile is the path that the template reads the output from.
func outputFile(name string) string { return outputsDir + "/" + nonIdent.ReplaceAllString(name, "_") }

// outputEpilogue is the script line that writes the value of an output into its file.
func outputEpilogue(name string) string {
	return "mkdir -p " + outputsDir + " && printf '%s' \"${" + outputEnv(name) + "}\" > " + outputFile(name)
}

// actionStep writes a step that uses an action. It returns a container when the action runs as one.
func (sx *stepCtx) actionStep(st *ir.Step, name string, last bool) *container {
	tc, e := sx.tc, sx.tc.e
	path := tc.jp.path + "/step/" + st.ID
	if st.Action == nil || st.Action.Kind != ir.ActionRepo {
		e.lose(path, caps.StepAction, "the action "+actionRef(st.Action)+" has no Argo form; the step is dropped")
		e.comment(path, "uses: "+actionRef(st.Action))
		return nil
	}
	with := map[string]string{}
	for k, v := range st.With {
		s, ok := valueCanon(v)
		if !ok {
			e.lose(path, caps.StepAction, "the input "+k+" of the action has no text form")
			continue
		}
		with[k] = s
	}
	t, ok := actions.Lookup(*st.Action, dialect, with)
	if !ok {
		e.lose(path, caps.StepAction, "the action "+actionRef(st.Action)+" has no Argo mapping; the step is dropped and the reference stays here as a comment")
		e.comment(path, "uses: "+actionRef(st.Action))
		return nil
	}
	e.note(path, caps.StepAction, t.Support.Level, t.Support.Strategy, t.Support.Caveat)
	for _, k := range t.Unmapped {
		e.lose(path, caps.StepAction, "the input "+k+" of the action has no Argo form")
	}
	if t.Support.Level == caps.Lost {
		return nil
	}
	switch t.Kind {
	case actions.KindImage:
		sx.image = t.Image
	case actions.KindImplicit:
		sx.checkout(st, with, t)
	case actions.KindUpload:
		sx.upload(path, t)
	case actions.KindDownload:
		sx.download(path, t)
	case actions.KindScript:
		cp := *st
		cp.Script, cp.Kind, cp.Action = t.Script, ir.StepScript, nil
		return sx.containerFor(&cp, name, last, nil)
	}
	return nil
}

// valueCanon renders a value as the canonical text the action registry reads.
func valueCanon(v ir.Value) (string, bool) {
	switch v.Kind {
	case ir.ValueSecret:
		return "${{ secrets." + v.Secret + " }}", true
	case ir.ValueExpr:
		parts := []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
		s, err := expr.PrintScriptTemplate(parts)
		return s, err == nil
	}
	return v.Lit, true
}

func actionRef(a *ir.ActionRef) string {
	if a == nil {
		return "(none)"
	}
	switch a.Kind {
	case ir.ActionDocker:
		return "docker://" + a.Image
	case ir.ActionLocal:
		return a.Path
	}
	s := a.Owner + "/" + a.Repo
	if a.Path != "" {
		s += "/" + a.Path
	}
	if a.Ref != "" {
		s += "@" + a.Ref
	}
	return s
}
