package concourse

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// stepSkip lists the step Ext keys that the emitter writes itself.
var stepSkip = map[string]bool{
	"file": true, ExtConfig: true, ExtParams: true, ExtShellFlags: true, ExtCommand: true, ExtArgs: true, ExtScriptLine: true,
	ExtEntrypoint: true, ExtImageResource: true, ExtRun: true, ExtKind: true, ExtStep: true,
}

// posixShells are the shells that run a script with -ec.
var posixShells = []string{"sh", "bash", "dash", "zsh", "ash"}

// chain is a run of tasks that pass the workspace along: the first task maps it from the source.
type chain struct {
	jx     *jobEmitter
	mapped bool
	pos    int
}

func (c *chain) steps(steps []*ir.Step) []*yaml.Node {
	var out []*yaml.Node
	for _, st := range steps {
		if st != nil {
			out = append(out, c.step(st)...)
		}
	}
	return out
}

// step writes one step as nodes of the plan.
func (c *chain) step(st *ir.Step) []*yaml.Node {
	jx := c.jx
	e := jx.pe.e
	path := jx.path + "/step/" + st.ID
	at := c.pos
	c.pos++
	if !e.own {
		e.foreignExt(path, st.Ext)
	}
	switch st.Kind {
	case ir.StepAction:
		return c.action(st, path, at)
	case ir.StepScript, "":
		return []*yaml.Node{c.script(st, path)}
	case ir.StepResource, ir.StepPlugin, ir.StepTask:
		if n := c.verbatim(st, path); n != nil {
			return []*yaml.Node{n}
		}
		return nil
	}
	e.lose(path, caps.ExtPassthrough, "the step kind "+string(st.Kind)+" has no Concourse form; the step is not written")
	return nil
}

// stepName gives a step its task name: the sanitised ID, made unique among the step and resource names of the job.
func (jx *jobEmitter) stepName(st *ir.Step, path string) string {
	name := unique(jx.taken, adapter{}.SanitizeID(st.ID))
	if name != st.ID {
		jx.pe.e.emulate(path, caps.ExtPassthrough, "step name changed", "the step ID "+st.ID+" is not a Concourse identifier or collides with another step or resource name; the task is "+name)
	}
	if st.Name != "" || name != st.ID {
		jx.pe.e.markArg(caps.DisplayName, jx.sm(name), nameArg{Name: st.Name, ID: st.ID, Written: name, Kind: "step"})
	}
	return name
}

// script writes a script step as a task.
func (c *chain) script(st *ir.Step, path string) *yaml.Node {
	jx := c.jx
	e := jx.pe.e
	ext := map[string]any(nil)
	if e.own {
		ext = extOf(st.Ext)
	}
	name := jx.stepName(st, path)
	dir := jx.runDir(st, path, name, ext)
	sc := jx.newScope(dir)
	sc.envParams(path, st.Env)
	sc.envParams(path, jx.j.Env)
	sc.envParams(path, jx.pe.pl.Env)
	body := sc.scriptLines(path, st.Script)
	script, disabled := jx.guarded(sc, path, name, st, body)
	if len(script) == 0 {
		script = []string{placeholderCmd}
	}
	if !disabled {
		script = append(slices.Clone(sc.prelude), script...)
	}
	m := newMap()
	m.SetStr("task", name)
	cfg := jx.config(sc, path, st, ext, script, dir)
	if !jx.ownInputs(ext) {
		if !c.mapped && jx.pe.primary.name != workspaceName {
			im := newMap()
			im.SetStr(workspaceName, jx.pe.primary.name)
			m.Set("input_mapping", im.N)
		}
		c.mapped = true
	}
	m.Set("config", cfg)
	if len(jx.j.Runner.Labels) > 0 {
		m.Set("tags", strSeq(jx.j.Runner.Labels))
	}
	if st.Timeout != nil {
		m.SetStr("timeout", durationText(st.Timeout))
	}
	if ext != nil {
		skip := stepSkip
		if !jx.ownInputs(ext) {
			skip = withKey(stepSkip, "input_mapping")
		}
		if err := m.AddExtra(ext, skip); err != nil {
			e.lose(path, caps.ExtPassthrough, "the Ext of the step cannot be written: "+err.Error())
		}
	}
	jx.secretMarker(sc, name)
	if !st.ContinueOnError {
		return m.N
	}
	e.emulate(path, caps.StepContinueOnError, "try around the task", "a failure of the task does not fail the job")
	try := newMap()
	try.Set("try", m.N)
	return try.N
}

// sm is the marker path of a task: the parser reads the written names, so the path holds them.
func (jx *jobEmitter) sm(task string) string { return jx.mpath + "/step/" + task }

func withKey(m map[string]bool, k string) map[string]bool {
	out := make(map[string]bool, len(m)+1)
	for key, v := range m {
		out[key] = v
	}
	out[k] = true
	return out
}

// ownInputs is true when a Concourse-origin task kept its own inputs or outputs, so no workspace is added.
func (jx *jobEmitter) ownInputs(ext map[string]any) bool {
	cfg, _ := ext[ExtConfig].(map[string]any)
	_, in := cfg["inputs"]
	_, out := cfg["outputs"]
	return in || out
}

// runDir moves a relative working directory under the workspace; a Concourse-origin task keeps its directory.
func (jx *jobEmitter) runDir(st *ir.Step, path, name string, ext map[string]any) string {
	if st.WorkDir == "" {
		return ""
	}
	e := jx.pe.e
	if parts, err := expr.ParseScriptTemplate(st.WorkDir); err != nil || len(parts) != 1 || parts[0].Expr != nil {
		e.lose(path, caps.StepWorkdir, "the working directory holds an expression, which a task dir cannot hold; the step runs in the task directory")
		return ""
	}
	if strings.Contains(st.WorkDir, varOpen) {
		e.lose(path, caps.StepWorkdir, "the working directory holds ((, which Concourse reads as a credential variable; the step runs in the task directory")
		return ""
	}
	if strings.HasPrefix(st.WorkDir, "/") || jx.ownInputs(ext) {
		return st.WorkDir
	}
	dir := workspaceName + "/" + strings.TrimPrefix(st.WorkDir, "./")
	e.emulate(path, caps.StepWorkdir, "under the workspace", "the relative working directory "+st.WorkDir+" is written as "+dir)
	e.markArg(caps.StepWorkdir, jx.sm(name), workdirArg{WorkDir: st.WorkDir, Wrote: dir})
	return dir
}

// guarded adds the guard of the job and step conditions before the script lines. A condition with no guard form
// disables the step: its first line is exit 0 and the original lines follow.
func (jx *jobEmitter) guarded(sc *scope, path, name string, st *ir.Step, body []string) (script []string, disabled bool) {
	e := jx.pe.e
	cond := st.If
	if cond != nil {
		if s, ok := statusOf(cond); ok && (s == "always" || s == "success") {
			cond = nil
		}
	}
	if jx.guard != nil && cond != nil {
		cond = expr.Binary{Op: expr.OpAnd, L: jx.guard, R: cond}
	} else if jx.guard != nil {
		cond = jx.guard
	}
	if cond == nil {
		return body, false
	}
	fail := func(why string) ([]string, bool) {
		e.lose(path, caps.StepIf, why+"; the step runs exit 0 first and does nothing")
		e.markArg(caps.StepIf, jx.sm(name), disabledArg{If: expr.String(cond), Script: strings.Join(body, "\n")})
		return append([]string{disabledScript}, body...), true
	}
	refs, ok := guardRefs(cond)
	if !ok || !guardable(cond) {
		return fail("the condition has no shell test form")
	}
	for _, r := range refs {
		env, ok := sc.refVar(r)
		if want, _ := exprconcourse.EnvName(r); !ok || env != want {
			return fail("the reference " + expr.String(r) + " has no spelling in a Concourse task")
		}
	}
	line, err := exprconcourse.PrintGuard(cond)
	if err != nil {
		return fail("the condition cannot be written as a guard: " + err.Error())
	}
	if st.If != nil {
		e.emulate(path, caps.StepIf, "shell guard", "the script starts with a guard line, so the task shows success when the condition is false")
	}
	return append([]string{line}, body...), false
}

func guardable(e expr.Expr) bool { return exprconcourse.Guardable(e) }

func guardRefs(e expr.Expr) ([]expr.Ref, bool) { return exprconcourse.GuardRefs(e) }

// secretMarker writes the marker for params whose kind differs from what their name says.
func (jx *jobEmitter) secretMarker(sc *scope, name string) {
	var a secretArg
	for _, p := range sc.params {
		m := wholeVar.FindStringSubmatch(p.value)
		if m == nil {
			continue
		}
		want, known := sc.intent[p.name]
		if known && want != secretName(m[1]) {
			if want {
				a.Secrets = append(a.Secrets, p.name)
			} else {
				a.Vars = append(a.Vars, p.name)
			}
		}
	}
	if len(a.Secrets)+len(a.Vars) > 0 {
		jx.pe.e.markArg(caps.ConfigName, jx.sm(name), a)
	}
}

// config writes the task config.
func (jx *jobEmitter) config(sc *scope, path string, st *ir.Step, ext map[string]any, script []string, dir string) *yaml.Node {
	e := jx.pe.e
	cfgExt, _ := ext[ExtConfig].(map[string]any)
	cfg := newMap()
	platform := jx.platform(cfgExt)
	cfg.SetStr("platform", platform)
	cfg.Set("image_resource", jx.image(sc, path, st, ext))
	if !jx.ownInputs(ext) {
		ws := func() *yaml.Node {
			m := newMap()
			m.SetStr("name", workspaceName)
			return seqOf(m.N)
		}
		cfg.Set("inputs", ws())
		cfg.Set("outputs", ws())
	}
	if len(jx.caches) > 0 {
		var cs []*yaml.Node
		for _, p := range jx.caches {
			m := newMap()
			m.SetStr("path", p)
			cs = append(cs, m.N)
		}
		cfg.Set("caches", seqOf(cs...))
	}
	params := newMap()
	for _, p := range sc.params {
		params.SetStr(p.name, p.value)
	}
	if raw, ok := ext[ExtParams].(map[string]any); ok {
		for _, k := range sortedKeys(raw) {
			if !params.Has(k) {
				params.Set(k, e.node(path, "param "+k, raw[k]))
			}
		}
	}
	if !params.Empty() {
		cfg.Set("params", params.N)
	}
	cfg.Set("run", jx.run(sc, path, st, ext, script, dir))
	if cfgExt != nil {
		if err := cfg.AddExtra(cfgExt, nil); err != nil {
			e.lose(path, caps.ExtPassthrough, "the task config Ext cannot be written: "+err.Error())
		}
	}
	return cfg.N
}

func (jx *jobEmitter) platform(cfgExt map[string]any) string {
	if p, _ := cfgExt["platform"].(string); p != "" {
		return p
	}
	switch p := jx.j.Runner.Platform; p {
	case "linux", "windows", "darwin":
		return p
	}
	return defaultPlatform
}

// run writes run.path, run.args and run.dir.
func (jx *jobEmitter) run(sc *scope, path string, st *ir.Step, ext map[string]any, script []string, dir string) *yaml.Node {
	e := jx.pe.e
	run := newMap()
	line, _ := ext[ExtScriptLine].(string)
	switch {
	case ext != nil && line != "" && strings.Join(st.Script, "\n") == line && st.If == nil && jx.guard == nil && len(sc.prelude) == 0 && !strings.Contains(line, "${{"):
		run.SetStr("path", strOf(ext[ExtCommand]))
		if args, ok := ext[ExtArgs].([]any); ok {
			n, _ := plainNode(args)
			run.Set("args", n)
		}
	default:
		flags := []string{defaultFlags}
		if raw, ok := ext[ExtShellFlags].([]any); ok && len(raw) > 0 {
			flags = flags[:0]
			for _, f := range raw {
				flags = append(flags, strOf(f))
			}
		}
		run.SetStr("path", jx.shellOf(st, path))
		run.Set("args", strSeq(append(flags, strings.Join(script, "\n"))))
	}
	if dir != "" {
		run.SetStr("dir", dir)
	}
	if extRun, ok := ext[ExtRun].(map[string]any); ok {
		if err := run.AddExtra(extRun, nil); err != nil {
			e.lose(path, caps.ExtPassthrough, "the run Ext cannot be written: "+err.Error())
		}
	}
	return run.N
}

func strOf(v any) string { s, _ := v.(string); return s }

// shellOf picks run.path for the shell of a step. A shell with no -ec form stays sh and is reported.
func (jx *jobEmitter) shellOf(st *ir.Step, path string) string {
	sh := strings.TrimSpace(st.Shell)
	base := sh
	if i := strings.LastIndex(sh, "/"); i >= 0 {
		base = sh[i+1:]
	}
	switch {
	case sh == "" || sh == shellCommand:
		return shellCommand
	case slices.Contains(posixShells, base):
		jx.pe.e.emulate(path, caps.StepShell, "run.path "+sh, "the task runs "+sh+" -ec, so the image must provide "+sh)
		return sh
	}
	jx.pe.e.lose(path, caps.StepShell, "the shell "+sh+" has no -ec form; the script runs under "+shellCommand)
	return shellCommand
}

// image writes image_resource: the step image, the image a setup action chose, the job image, the image_resource a
// Concourse file held, or the default image.
func (jx *jobEmitter) image(sc *scope, path string, st *ir.Step, ext map[string]any) *yaml.Node {
	e := jx.pe.e
	for _, ref := range []string{refOf(st.Image), jx.img, refOf(jx.j.Container)} {
		if ref == "" {
			continue
		}
		if n, ok := jx.imageNode(sc, ref); ok {
			return n
		}
		e.lose(path, caps.StepImage, "the image "+ref+" has no Concourse spelling; the default image "+defaultImage+":"+defaultTag+" runs")
		return imageNode(defaultImage, "", defaultTag)
	}
	if raw, ok := ext[ExtImageResource]; ok {
		if n := e.node(path, "image_resource", raw); n != nil {
			return n
		}
	}
	e.emulate(path, caps.StepImage, "default image", "the task names no image, so it runs "+defaultImage+":"+defaultTag)
	return imageNode(defaultImage, "", defaultTag)
}

func refOf(i *ir.Image) string {
	if i == nil {
		return ""
	}
	return i.Ref
}

// imageNode converts an image reference to repository and tag; text from a matrix is a local var.
func (jx *jobEmitter) imageNode(sc *scope, ref string) (*yaml.Node, bool) {
	parts, err := expr.ParseScriptTemplate(ref)
	if err != nil {
		return nil, false
	}
	var sb strings.Builder
	for _, p := range parts {
		switch x := p.Expr.(type) {
		case nil:
			sb.WriteString(p.Text)
		case expr.Lit:
			sb.WriteString(litText(x))
		case expr.Ref:
			t, _, ok := sc.paramRef(x)
			if !ok || len(x.Path) != 2 || x.Path[0] != expr.PrefixMatrix {
				return nil, false
			}
			sb.WriteString(t)
		default:
			return nil, false
		}
	}
	out := sb.String()
	if strings.Contains(out, varOpen) && !strings.Contains(out, "((.:") {
		return nil, false
	}
	repo, tag, digest := splitImage(out)
	return imageNode(repo, digest, tag), true
}

// splitImage splits a reference into repository, tag and digest.
func splitImage(ref string) (repo, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], "", ref[i+1:]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:], ""
	}
	return ref, "", ""
}

func imageNode(repo, digest, tag string) *yaml.Node {
	src := newMap()
	src.SetStr("repository", repo)
	if tag != "" {
		src.SetStr("tag", tag)
	}
	if digest != "" {
		src.SetStr("digest", digest)
	}
	m := newMap()
	m.SetStr("type", "registry-image")
	m.Set("source", src.N)
	return m.N
}
