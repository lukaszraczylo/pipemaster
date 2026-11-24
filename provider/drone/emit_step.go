package drone

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	placeholderName = "noop"
	// noopScript runs in a step that only keeps the document valid and stands for no source work.
	noopScript    = "true"
	keyEnv        = "environment"
	keySettings   = "settings"
	keyCommands   = "commands"
	keyDependsOn  = "depends_on"
	keyWorkingDir = "working_dir"
	keyEntrypoint = "entrypoint"
	keyFailure    = "failure"
	// defaultImage runs steps that name no image; the IR does not hold the runner image of a hosted runner.
	defaultImage = "ubuntu:latest"
)

// stepDone is one emitted step: its map and the IR ID it came from.
type stepDone struct {
	m   *ymap
	src string
}

// commandShape records how script lines group into one command.
type commandShape struct {
	Lines int
	Final bool
}

func numOf(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), n == float64(int(n))
	}
	return 0, false
}

// commandTexts groups the script lines of a step into commands. The Ext of a step that came from Drone says how
// its lines group; any other step has one command per line.
func commandTexts(st *ir.Step) []string {
	raw, _ := extOf(st.Ext)[ExtCommands].([]any)
	var shapes []commandShape
	total := 0
	for _, r := range raw {
		m, ok := r.(map[string]any)
		n, isNum := numOf(m["lines"])
		if !ok || !isNum || n < 1 {
			return foldBlankLines(st.Script)
		}
		final, _ := m["final"].(bool)
		shapes = append(shapes, commandShape{Lines: n, Final: final})
		total += n
	}
	if len(shapes) == 0 || total != len(st.Script) {
		return foldBlankLines(st.Script)
	}
	var out []string
	at := 0
	for _, s := range shapes {
		text := strings.Join(st.Script[at:at+s.Lines], "\n")
		if s.Final {
			text += "\n"
		}
		out = append(out, text)
		at += s.Lines
	}
	return out
}

// foldBlankLines makes one command per line and joins each blank line to the command before it, because a Drone
// command must not be empty. A blank line before the first command joins the one after it.
func foldBlankLines(lines []string) []string {
	var out []string
	lead := ""
	trailing := false
	for _, l := range lines {
		switch {
		case l != "":
			out = append(out, lead+l)
			lead, trailing = "", false
		case len(out) == 0:
			lead += "\n"
		case trailing:
			out[len(out)-1] += "\n"
		default:
			out[len(out)-1] += "\n\n"
			trailing = true
		}
	}
	return out
}

// steps writes the step list. Names are the IDs made valid and unique; each rename is recorded for EmittedIDs.
func (e *emitter) steps(c *docCtx) (*yaml.Node, error) {
	var ids []string
	for _, st := range c.j.Steps {
		if st != nil {
			ids = append(ids, st.ID)
		}
	}
	names := uniqueNames(ids, validStepName)
	c.written = map[string]string{}
	var done []stepDone
	at := 0
	for _, st := range c.j.Steps {
		if st == nil {
			continue
		}
		name := names[at]
		at++
		e.scriptLost = false
		c.pos = len(done)
		d, err := e.step(c, st, name)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", st.ID, err)
		}
		if d.m == nil {
			continue
		}
		c.written[st.ID] = name
		if name != st.ID {
			e.stepIDs[pipelinePth+"/job/"+c.name+"/step/"+name] = st.ID
		}
		c.hold(st, len(done), e.scriptLost)
		done = append(done, d)
	}
	if len(done) == 0 {
		d, err := e.placeholder(c)
		if err != nil {
			return nil, err
		}
		done = append(done, d)
	}
	e.dependencies(c, done)
	n := yseq()
	for _, d := range done {
		n.Content = append(n.Content, d.m.n)
	}
	return n, nil
}

// hold records the text of a step of the first matrix cell that reads a matrix name, so the parser can put the
// references back.
func (c *docCtx) hold(st *ir.Step, pos int, scriptLost bool) {
	if c.j.Matrix == nil || !c.plan.first {
		return
	}
	h := heldText{Pos: pos}
	if st.Image != nil && readsMatrix(st.Image.Ref) {
		h.Image = st.Image.Ref
	}
	if readsMatrix(st.WorkDir) {
		h.WorkDir = st.WorkDir
	}
	if !scriptLost && slices.ContainsFunc(st.Script, readsMatrix) {
		h.Script = st.Script
	}
	if env := slices.DeleteFunc(slices.Clone(st.Env), func(v ir.Var) bool { return !valueReadsMatrix(v.Value) }); len(env) > 0 {
		h.Env = heldYAML(env)
	}
	with := map[string]ir.Value{}
	for k, v := range st.With {
		if valueReadsMatrix(v) {
			with[k] = v
		}
	}
	if len(with) > 0 {
		h.With = heldYAML(with)
	}
	if h.Image != "" || h.WorkDir != "" || h.Script != nil || h.Env != "" || h.With != "" {
		c.held = append(c.held, h)
	}
}

// heldYAML carries v as YAML. A value that does not encode is carried as nothing, and the parser keeps the cell text.
func heldYAML(v any) string {
	y, err := carryYAML(v)
	if err != nil {
		return ""
	}
	return y
}

// valueReadsMatrix reports whether the value is an expression that reads a matrix name.
func valueReadsMatrix(v ir.Value) bool {
	parts, ok := valueParts(v)
	return ok && slices.ContainsFunc(parts, func(p expr.Part) bool {
		r, isRef := p.Expr.(expr.Ref)
		return isRef && len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix
	})
}

// valueParts reads an expression value as template parts, as the emitter spells it.
func valueParts(v ir.Value) ([]expr.Part, bool) {
	if v.Kind != ir.ValueExpr || v.Expr == nil {
		return nil, false
	}
	if c, ok := v.Expr.(expr.Call); ok {
		if mixed, ok := emitkit.UnmixFormat(c); ok {
			return mixed, true
		}
	}
	return []expr.Part{{Expr: v.Expr}}, true
}

// readsMatrix reports whether the template text holds a reference to a matrix name.
func readsMatrix(s string) bool {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(parts, func(p expr.Part) bool {
		r, ok := p.Expr.(expr.Ref)
		return ok && len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix
	})
}

// dependencies writes depends_on on every step once one step has it: Drone needs it on all steps (research
// drone.md 3). A step without a list is a root.
func (e *emitter) dependencies(c *docCtx, done []stepDone) {
	has := false
	deps := map[string][]string{}
	for _, st := range c.j.Steps {
		if st == nil {
			continue
		}
		if raw, ok := extOf(st.Ext)[keyDependsOn]; ok {
			has = true
			deps[st.ID] = listOf2(raw)
		}
	}
	if !has {
		return
	}
	for _, d := range done {
		out := []string{}
		for _, dep := range deps[d.src] {
			switch w, wrote := c.written[dep]; {
			case wrote:
				out = append(out, w)
			case !slices.ContainsFunc(c.j.Steps, func(s *ir.Step) bool { return s != nil && s.ID == dep }):
				out = append(out, dep)
			default:
				e.lose(c.jp+stepSep+d.src, caps.ExtPassthrough, "depends_on names the step "+dep+", which is not written; the dependency is dropped")
			}
		}
		d.m.set(keyDependsOn, ystrSeq(out))
	}
}

// listOf2 reads a scalar or a list of scalars from Ext.
func listOf2(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	}
	return nil
}

func (e *emitter) step(c *docCtx, st *ir.Step, name string) (stepDone, error) {
	sp := c.jp + "/step/" + st.ID
	e.foreignExt(sp, st.Ext)
	e.cut = false
	var m *ymap
	var err error
	carried := false
	switch {
	case st.Action != nil || st.Kind == ir.StepAction || st.Kind == ir.StepPipelineCall:
		m, err = e.actionStep(c, sp, st)
		carried = true
	case st.Kind == ir.StepPlugin:
		m, err = e.pluginStep(c, sp, st)
	case st.Kind == ir.StepScript || len(st.Script) > 0:
		m, err = e.scriptStep(c, sp, st)
	default:
		why := "step kind " + string(st.Kind) + " has no Drone form"
		e.lose(sp, caps.StepAction, why)
		e.comment("step " + sp + " kind " + string(st.Kind))
		m, err = e.failingStep(c, sp, st, caps.StepAction, why)
		carried = true
	}
	if err != nil {
		return stepDone{}, err
	}
	if m == nil {
		if !carried {
			e.unapplied(sp, st)
			err = e.markAction(sp, st, c.pos, "", false)
		}
		return stepDone{}, err
	}
	return e.finishStep(c, sp, st, name, m)
}

// finishStep adds the name, the failure policy, the condition and the Ext keys.
func (e *emitter) finishStep(c *docCtx, sp string, st *ir.Step, name string, body *ymap) (stepDone, error) {
	ext := extOf(st.Ext)
	m := newYMap()
	m.set("name", ystr(name))
	if st.Name != "" && st.Name != st.ID {
		e.lose(sp, caps.DisplayName, "a Drone step has one name, which carries the id; the display name is kept in round-trip metadata")
	}
	for i := 0; i+1 < len(body.n.Content); i += 2 {
		m.set(body.n.Content[i].Value, body.n.Content[i+1])
	}
	if c.j.AllowFailure || st.ContinueOnError {
		m.set(keyFailure, ystr(failureIgnore))
	}
	if e.cut {
		e.cut = false
		e.lose(sp, caps.StepIf, "an image or workdir lost an expression, so the step is disabled")
		e.comment("step " + sp + ": an image or workdir lost an expression, so the step is disabled")
		n, err := condNode(disabledSet(), nil)
		if err != nil {
			return stepDone{}, err
		}
		m.set(keyWhen, n)
	} else {
		n, _, err := e.stepWhen(sp, st, len(st.Script) > 0 && body.has(keyCommands) && !windowsJob(c.j))
		if err != nil {
			return stepDone{}, err
		}
		m.set(keyWhen, n)
	}
	skip := map[string]bool{keyEnv: true, keySettings: true, keyWhen: true, keyDependsOn: true}
	return stepDone{m: m, src: st.ID}, m.addExtra(ext, skip)
}

// stepImage is the image of a step: its own, else the one a setup action or the job container gave, else the default.
func (e *emitter) stepImage(c *docCtx, sp string, st *ir.Step) (string, error) {
	ref := c.image
	if st.Image != nil && st.Image.Ref != "" {
		ref = st.Image.Ref
	} else if ref != "" && c.j.Container != nil && ref == c.j.Container.Ref {
		c.took++
	}
	if ref == "" {
		if !c.defImg {
			c.defImg = true
			e.emulate(c.jp, caps.JobContainer, "default image "+defaultImage, "the job names no image, so steps without one run in "+defaultImage)
			e.mark(caps.JobContainer, c.jp)
		}
		return defaultImage, nil
	}
	return e.text(nil, sp, caps.StepImage, valueText, ref)
}

// stepEnv builds the environment of a step: its own variables, the secrets its text reads, then the job
// variables that a document-level map cannot hold.
func (e *emitter) stepEnv(c *docCtx, sp string, st *ir.Step, b *bindings) (*yaml.Node, error) {
	env := newYMap()
	if err := e.envNode(sp, st.Env, env); err != nil {
		return nil, err
	}
	if err := env.mergeMap(extOf(st.Ext)[keyEnv]); err != nil {
		return nil, err
	}
	bind := func(names []string) {
		for _, name := range names {
			if env.has(name) {
				continue
			}
			s := newYMap()
			s.set(fromSecret, ystr(name))
			env.set(name, s.n)
		}
	}
	for _, name := range b.secrets {
		if !env.has(name) {
			e.emulate(sp, caps.ScriptInterpolation, "environment from_secret", "secret "+name+" is bound to the variable "+name+", which the command text reads at run time")
		}
	}
	bind(b.secrets)
	for _, name := range b.vars {
		if !env.has(name) {
			e.external(sp, caps.ScriptInterpolation, "secret for variable", "variable "+name+" is bound to the secret "+name+"; Drone has no variables, so create the secret "+name+" with the value of the variable")
		}
	}
	bind(b.vars)
	for _, name := range b.inputs {
		e.external(sp, caps.ScriptInterpolation, "environment variable for input", "input "+name+" is read as the variable "+name+", which nothing sets; define it in the Drone environment")
	}
	if len(b.refs) > 0 {
		if err := e.markArg(caps.ScriptInterpolation, sp, posArg{Pos: c.pos, Refs: b.refs, At: b.at}); err != nil {
			return nil, err
		}
	}
	if err := e.envNode(c.jp, c.secretVars, env); err != nil {
		return nil, err
	}
	if env.empty() {
		return nil, nil
	}
	return env.n, nil
}

// common sets the image and the entrypoint. A command step takes no entrypoint: Drone replaces it with the
// script runner whenever commands exist (drone-runner-docker compiler/script.go setupScriptPosix assigns
// dst.Entrypoint after createStep copied src.Entrypoint). An empty entrypoint then changes nothing, so it is
// not written; any other value would be dropped by Drone, so it is reported as lost.
func (e *emitter) common(c *docCtx, sp string, st *ir.Step, m *ymap, commands bool) error {
	img, err := e.stepImage(c, sp, st)
	if err != nil {
		return err
	}
	m.set(keyImage, ystr(img))
	if st.Image == nil || len(st.Image.Entrypoint) == 0 {
		return nil
	}
	if commands {
		if slices.ContainsFunc(st.Image.Entrypoint, func(s string) bool { return s != "" }) {
			e.lose(sp, caps.JobContainer, "Drone replaces the entrypoint of a command step with the script runner, so the entrypoint is not written")
		} else {
			e.lose(sp, caps.StepImage, "an empty entrypoint changes nothing on a Drone command step, so it is not written")
		}
		return nil
	}
	ep := make([]string, len(st.Image.Entrypoint))
	for i, s := range st.Image.Entrypoint {
		t, err := e.text(nil, sp, caps.StepImage, valueText, s)
		if err != nil {
			return err
		}
		ep[i] = t
	}
	m.set(keyEntrypoint, ystrSeq(ep))
	return nil
}

func (e *emitter) scriptStep(c *docCtx, sp string, st *ir.Step) (*ymap, error) {
	b := &bindings{}
	var lines []*yaml.Node
	seen := map[string]int{}
	texts := slices.Clone(commandTexts(st))
	why := make([]string, len(texts))
	for i, l := range texts {
		texts[i] = emitkit.NeutralizeResultWrite(e.lose, sp, l)
		_, lost, err := e.command(&bindings{}, texts[i])
		if err != nil {
			return nil, err
		}
		why[i] = noSpelling(lost)
	}
	for _, p := range e.commentSpans(sp, texts, why) {
		if p.Lost {
			e.scriptLost = true
			lines = append(lines, block(emitkit.FailLoud(caps.ScriptInterpolation, sp, p.Detail)))
			continue
		}
		b.hits = nil
		out, _, err := e.command(b, texts[p.Index])
		if err != nil {
			return nil, err
		}
		lines = append(lines, block(out))
		b.place(out, seen)
	}
	b.total(seen)
	if len(lines) == 0 {
		e.comment("step.script " + sp + ": the step has no command left")
		e.lose(sp, caps.StepScript, "no command of the step has a Drone spelling; the step is not written")
		return nil, nil
	}
	guards, err := e.guards(c, st)
	if err != nil {
		return nil, err
	}
	lines = append(guards, lines...)
	m := newYMap()
	if err := e.common(c, sp, st, m, true); err != nil {
		return nil, err
	}
	m.set(keyCommands, yseq(lines...))
	if err := e.stepTail(c, sp, st, b, m); err != nil {
		return nil, err
	}
	e.stepLosses(sp, st)
	return m, nil
}

// guards returns the guard lines of a script step: the job guard first, then the step guard.
func (e *emitter) guards(c *docCtx, st *ir.Step) ([]*yaml.Node, error) {
	var out []*yaml.Node
	if st.If != nil && !windowsJob(c.j) {
		if _, filter := e.exprSet(st.If); !filter {
			if ok, _ := e.guardable(st.If); ok {
				line, err := e.printGuard(st.If)
				if err != nil {
					return nil, err
				}
				out = append(out, ystr(esc(line)))
			}
		}
	}
	if c.guard != nil {
		line, err := e.printGuard(c.guard)
		if err != nil {
			return nil, err
		}
		out = append([]*yaml.Node{ystr(esc(line))}, out...)
	}
	return out, nil
}

// printGuard writes the guard line of x, reading a folded comparison case-sensitively when it must.
func (e *emitter) printGuard(x expr.Expr) (string, error) {
	x = e.toDrone(x)
	if dexpr.Guardable(x, e.sem) {
		return dexpr.PrintGuard(x, e.sem)
	}
	return dexpr.PrintGuard(x, e.sem, dexpr.AllowFold)
}

// stepTail adds the environment and the working directory.
func (e *emitter) stepTail(c *docCtx, sp string, st *ir.Step, b *bindings, m *ymap) error {
	env, err := e.stepEnv(c, sp, st, b)
	if err != nil {
		return err
	}
	m.set(keyEnv, env)
	if st.WorkDir != "" {
		d, err := e.text(nil, sp, caps.StepWorkdir, valueText, st.WorkDir)
		if err != nil {
			return err
		}
		m.set(keyWorkingDir, ystr(d))
	}
	return nil
}

// stepLosses reports the step fields Drone has no key for.
func (e *emitter) stepLosses(sp string, st *ir.Step) {
	if st.Timeout != nil {
		e.lose(sp, caps.StepTimeout, "the Drone docker runner ignores a step timeout")
	}
	if st.Shell != "" {
		e.lose(sp, caps.StepShell, "Drone compiles commands into its own script, not "+st.Shell)
	}
	if strings.TrimSpace(st.Shell) == "" && len(st.Script) > 0 && provider.BashDefault(e.origin) {
		e.emulate(sp, caps.StepShell, "commands run under the image sh", "the source runs the step under bash when the image has it; Drone compiles commands into a script that its runner pipes to /bin/sh, and the entrypoint cannot change it, so bash-only syntax fails when the image sh is dash")
	}
}

func (e *emitter) pluginStep(c *docCtx, sp string, st *ir.Step) (*ymap, error) {
	if st.Image == nil || st.Image.Ref == "" {
		why := "a plugin step needs an image"
		e.lose(sp, caps.StepAction, why)
		e.comment("step " + sp + ": plugin without an image")
		return e.failingStep(c, sp, st, caps.StepAction, why)
	}
	m := newYMap()
	if err := e.common(c, sp, st, m, false); err != nil {
		return nil, err
	}
	set := newYMap()
	for _, k := range sortedKeys(st.With) {
		n, err := e.settingNode(sp, st.With[k])
		if err != nil {
			return nil, err
		}
		set.set(k, n)
	}
	if err := set.mergeMap(extOf(st.Ext)[keySettings]); err != nil {
		return nil, err
	}
	if !set.empty() {
		m.set(keySettings, set.n)
	}
	if err := e.stepTail(c, sp, st, &bindings{}, m); err != nil {
		return nil, err
	}
	e.stepLosses(sp, st)
	return m, nil
}

// settingNode writes a plugin setting; a literal boolean or integer keeps its YAML type.
func (e *emitter) settingNode(sp string, v ir.Value) (*yaml.Node, error) {
	if v.Kind == ir.ValueLit {
		return scalarOf(esc(v.Lit)), nil
	}
	return e.valueNode(sp, v)
}

// placeholder is the one step of a document that has none Drone can run; Drone rejects a pipeline without steps.
func (e *emitter) placeholder(c *docCtx) (stepDone, error) {
	script := noopScript
	if e.lostStepWork(c.jp) {
		script = emitkit.FailLoud(caps.StepScript, c.jp, "no step of the job has a Drone form")
		e.emulate(c.jp, caps.StepScript, "failing step", "Drone needs a step; no step of the job has a Drone form, so a step that prints the loss and exits 1 stands in")
	} else {
		e.emulate(c.jp, caps.StepScript, "no-op step", "Drone needs a step; the job has no step to run, so a no-op step stands in")
	}
	e.mark(caps.StepScript, c.jp)
	why := "the job has no steps; a no-op step keeps the document valid"
	if script != noopScript {
		why = "no step of the job has a Drone form; a step that prints the loss and exits 1 keeps the document valid and the pipeline honest"
	}
	e.note(c.jp+"/step/script", caps.StepScript, caps.Lost, "", why)
	m := newYMap()
	m.set("name", ystr(placeholderName))
	img, err := e.stepImage(c, c.jp, &ir.Step{})
	if err != nil {
		return stepDone{}, err
	}
	m.set(keyImage, ystr(img))
	m.set(keyCommands, ystrSeq([]string{script}))
	return stepDone{m: m}, nil
}

// validStepName is true for a step name that Drone files use as written. Real files name steps with spaces and
// colons, and Drone reads the name as free text; a dollar sign would start a substitution, so it is not kept.
func validStepName(s string) bool {
	return s != "" && len(s) <= maxName && s == strings.TrimSpace(s) &&
		!strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r > '~' || r == '$' })
}

// uniqueNames makes each base a valid name and gives duplicates a -2, -3 suffix. A base that is already valid, by
// the strict rule or by valid, keeps its name, so the result never depends on map order.
func uniqueNames(bases []string, valid func(string) bool) []string {
	a := adapter{}
	out := make([]string, len(bases))
	taken := map[string]bool{}
	for i, b := range bases {
		if (a.SanitizeID(b) == b || valid != nil && valid(b)) && !taken[b] {
			out[i], taken[b] = b, true
		}
	}
	for i, b := range bases {
		if out[i] != "" {
			continue
		}
		base := a.SanitizeID(b)
		cand := base
		for n := 2; taken[cand]; n++ {
			suffix := "-" + strconv.Itoa(n)
			trunc := base
			if len(trunc)+len(suffix) > maxName {
				trunc = trunc[:maxName-len(suffix)]
			}
			cand = trunc + suffix
		}
		out[i], taken[cand] = cand, true
	}
	return out
}
