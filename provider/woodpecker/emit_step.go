package woodpecker

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	placeholderName = "noop"
	stepKey         = "step-"
	failKey         = "failure"
	envKey          = "environment"
	settKey         = "settings"
	cmdsKey         = "commands"
	imageKey        = "image"
	dirKey          = "directory"
	nameKey         = "name"
	entryKey        = "entrypoint"
	extDerive       = ExtDerivedID
)

// stepDone is one emitted step: the node and the map key it has in map form.
type stepDone struct {
	node *yaml.Node
	key  string
	// src is the step ID of the IR; read is the ID a reader derives for the step, empty when the written name is
	// the ID. A step without a written name takes its position, which steps fills in.
	src, read string
	anon      bool
}

// derived reports whether any provider marked the step ID as made up from the step position.
func derived(st *ir.Step) bool {
	for _, raw := range st.Ext {
		if m, ok := raw.(map[string]any); ok && m[extDerive] == true {
			return true
		}
	}
	return false
}

// steps writes the step list; a map-form job (a step has a _map_key) keeps its map form.
func (e *emitter) steps(c *wfCtx) (*yaml.Node, error) {
	var done []stepDone
	mapForm := false
	for _, st := range c.j.Steps {
		if st != nil {
			_, has := extOf(st.Ext)["_map_key"]
			mapForm = mapForm || has
		}
	}
	c.mapForm = mapForm
	for i, st := range c.j.Steps {
		if st == nil {
			continue
		}
		c.pos = len(done)
		d, err := e.step(c, st, i)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", st.ID, err)
		}
		if d.node == nil {
			continue
		}
		done = append(done, d)
	}
	for pos, d := range done {
		if d.anon {
			d.read = stepKey + strconv.Itoa(pos)
		}
		if d.read != "" && d.read != d.src {
			e.stepIDs[c.jp+"/step/"+d.read] = d.src
		}
	}
	if len(done) == 0 {
		d, err := e.placeholder(c)
		if err != nil {
			return nil, err
		}
		done = append(done, d)
		mapForm = false
	}
	if mapForm {
		m := newMap()
		for _, d := range done {
			m.set(d.key, d.node)
		}
		return m.n, nil
	}
	n := seqOf()
	for _, d := range done {
		n.Content = append(n.Content, d.node)
	}
	return n, nil
}

// stepName picks the Woodpecker name: the ID, or the display name when the ID was made up from the position.
func (e *emitter) stepName(sp string, st *ir.Step) string {
	if derived(st) {
		name, err := e.text(nil, sp, caps.DisplayName, valueText, st.Name)
		if err != nil {
			e.lose(sp, caps.DisplayName, "the display name is not valid template text and is dropped")
			return ""
		}
		return name
	}
	if st.Name != "" && st.Name != st.ID {
		e.lose(sp, caps.DisplayName, "a Woodpecker step has one name, which carries the id; the display name is kept in round-trip metadata")
	}
	return st.ID
}

func (e *emitter) step(c *wfCtx, st *ir.Step, idx int) (stepDone, error) {
	sp := c.jp + "/step/" + st.ID
	e.foreignExt(sp, st.Ext)
	e.cut = false
	var m *wmap
	var err error
	carried := false
	switch {
	case st.Action != nil || st.Kind == ir.StepAction || st.Kind == ir.StepPipelineCall:
		m, err = e.actionStep(c, sp, st, idx)
		carried = true
	case st.Kind == ir.StepPlugin:
		m, err = e.pluginStep(c, sp, st)
	case st.Kind == ir.StepScript || len(st.Script) > 0:
		m, err = e.scriptStep(c, sp, st)
	default:
		e.lose(sp, caps.StepAction, "step kind "+string(st.Kind)+" has no Woodpecker form")
		e.comment("step " + sp + " kind " + string(st.Kind))
	}
	if err != nil {
		return stepDone{}, err
	}
	if m == nil {
		if !carried {
			e.unapplied(sp, st)
			err = e.markAction(sp, st, idx, "")
		}
		return stepDone{}, err
	}
	return e.finishStep(c, sp, st, idx, m)
}

// finishStep adds the name, the condition and the Ext keys, and orders the keys of the step.
func (e *emitter) finishStep(c *wfCtx, sp string, st *ir.Step, idx int, body *wmap) (stepDone, error) {
	ext := extOf(st.Ext)
	m := newMap()
	key := st.ID
	anon := false
	if c.mapForm {
		if k, ok := ext["_map_key"].(string); ok {
			key = k
		}
		if st.ID != key && !derived(st) {
			m.set(nameKey, str(st.ID))
		}
		if derived(st) {
			key = stepKey + strconv.Itoa(idx)
		}
	} else {
		name := e.stepName(sp, st)
		anon = name == ""
		if name != "" {
			m.set(nameKey, str(name))
		}
		if derived(st) && name != "" {
			arg := posArg{Pos: c.pos, Name: st.Name}
			if name != st.Name {
				arg.Wrote = name
			}
			if err := e.markArg(caps.DisplayName, sp, arg); err != nil {
				return stepDone{}, err
			}
		}
		if key == "" || derived(st) {
			key = stepKey + strconv.Itoa(idx)
		}
	}
	for i := 0; i+1 < len(body.n.Content); i += 2 {
		m.set(body.n.Content[i].Value, body.n.Content[i+1])
	}
	if c.j.AllowFailure || st.ContinueOnError {
		m.set(failKey, str("ignore"))
	}
	if e.cut {
		e.cut = false
		e.comment("step " + sp + ": an image or workdir lost an expression, so the step is disabled")
		m.set(keyWhen, whenNode(neverEntries(nil)))
	} else if raw, ok := ext[keyWhen]; ok {
		n, err := plainNode(raw)
		if err != nil {
			return stepDone{}, err
		}
		m.set(keyWhen, n)
	} else {
		m.set(keyWhen, e.stepWhen(sp, st.If))
	}
	skip := map[string]bool{envKey: true, settKey: true, keyWhen: true}
	d := stepDone{node: m.n, key: key, src: st.ID, anon: anon}
	if c.mapForm && key != st.ID {
		d.read = key
	}
	return d, m.addExtra(ext, skip)
}

// stepImage is the image of a step: its own, else the one a setup action or the job container gave, else the default.
func (e *emitter) stepImage(c *wfCtx, sp string, st *ir.Step) (string, error) {
	ref := c.image
	if st.Image != nil && st.Image.Ref != "" {
		ref = st.Image.Ref
	}
	if ref == "" {
		ref = defaultImage
		if !c.defImg {
			c.defImg = true
			e.emulate(c.jp, caps.JobContainer, "default image "+defaultImage, "the job names no image, so steps without one run in "+defaultImage)
			e.mark(caps.JobContainer, c.jp)
		}
		return ref, nil
	}
	return e.text(nil, sp, caps.StepImage, valueText, ref)
}

// stepEnv builds the environment of a step: its own variables, the secrets its text reads, then the job and pipeline variables.
func (e *emitter) stepEnv(c *wfCtx, sp string, st *ir.Step, b *bindings, ext map[string]any) (*yaml.Node, error) {
	env := newMap()
	if err := e.stepOwnEnv(c, sp, st, env); err != nil {
		return nil, err
	}
	if err := e.markShadows(c, sp, st); err != nil {
		return nil, err
	}
	if err := env.mergeMap(ext[envKey]); err != nil {
		return nil, err
	}
	for _, name := range b.secrets {
		if env.has(name) {
			continue
		}
		s := newMap()
		s.set(fromSecret, str(name))
		env.set(name, s.n)
		e.emulate(sp, caps.ScriptInterpolation, "environment from_secret", "secret "+name+" is bound to the variable "+name+", which the command text reads at run time")
	}
	for _, name := range b.vars {
		if env.has(name) {
			continue
		}
		s := newMap()
		s.set(fromSecret, str(name))
		env.set(name, s.n)
		e.external(sp, caps.ScriptInterpolation, "secret for variable", "variable "+name+" is bound to the secret "+name+"; Woodpecker has no variables, so create the secret "+name+" with the value of the variable")
	}
	for _, name := range b.inputs {
		e.external(sp, caps.ScriptInterpolation, "environment variable for input", "input "+name+" is read as the variable "+name+", which nothing sets; define it in the Woodpecker environment")
	}
	if len(b.refs) > 0 {
		if err := e.markArg(caps.ScriptInterpolation, sp, posArg{Pos: c.pos, Refs: b.refs}); err != nil {
			return nil, err
		}
	}
	if err := e.envNode(c.jp, c.j.Env, env); err != nil {
		return nil, err
	}
	if err := e.envNode("pipeline/"+c.pl.ID, c.pl.Env, env); err != nil {
		return nil, err
	}
	if env.empty() {
		return nil, nil
	}
	return env.n, nil
}

// stepOwnEnv writes the variables of the step. One whose value has no Woodpecker spelling goes into a marker.
func (e *emitter) stepOwnEnv(c *wfCtx, sp string, st *ir.Step, env *wmap) error {
	var lost []ir.Var
	var at []int
	for i, v := range st.Env {
		if env.has(v.Name) {
			continue
		}
		n, err := e.valueNode(sp, v.Value)
		if err != nil {
			return err
		}
		if n == nil {
			lost, at = append(lost, v), append(at, i)
			continue
		}
		env.set(v.Name, n)
	}
	if len(lost) == 0 {
		return nil
	}
	return e.markLostEnv(sp, c.pos, at, lost)
}

// common sets the keys every step kind shares; the caller adds commands or settings between them.
func (e *emitter) common(c *wfCtx, sp string, st *ir.Step, m *wmap) error {
	img, err := e.stepImage(c, sp, st)
	if err != nil {
		return err
	}
	m.set(imageKey, str(img))
	if st.Image != nil && len(st.Image.Entrypoint) > 0 {
		ep, err := e.entrypoint(sp, st.Image.Entrypoint)
		if err != nil {
			return err
		}
		m.set(entryKey, ep)
		return nil
	}
	return nil
}

func (e *emitter) scriptStep(c *wfCtx, sp string, st *ir.Step) (*wmap, error) {
	b := &bindings{}
	var lines []*yaml.Node
	for _, l := range commandTexts(st) {
		out, keep, err := e.command(b, sp, l)
		if err != nil {
			return nil, err
		}
		if keep {
			lines = append(lines, block(out))
		}
	}
	if len(lines) == 0 {
		e.comment("step.script " + sp + ": the step has no command left")
		e.lose(sp, caps.StepScript, "no command of the step has a Woodpecker spelling; the step is not written")
		return nil, nil
	}
	m := newMap()
	if err := e.common(c, sp, st, m); err != nil {
		return nil, err
	}
	m.set(cmdsKey, seqOf(lines...))
	if err := e.stepTail(c, sp, st, b, m); err != nil {
		return nil, err
	}
	e.stepLosses(sp, st)
	return m, nil
}

// commandTexts groups the script lines of a step into commands. The Ext of a step that came from Woodpecker
// says how its lines group; any other step has one command per line.
func commandTexts(st *ir.Step) []string {
	raw, _ := extOf(st.Ext)[ExtCommands].([]any)
	var shapes []commandShape
	total := 0
	for _, r := range raw {
		m, ok := r.(map[string]any)
		n, isNum := numOf(m["lines"])
		if !ok || !isNum || n < 1 {
			return st.Script
		}
		final, _ := m["final"].(bool)
		shapes = append(shapes, commandShape{Lines: n, Final: final})
		total += n
	}
	if len(shapes) == 0 || total != len(st.Script) {
		return st.Script
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

// stepTail adds the environment and the working directory.
func (e *emitter) stepTail(c *wfCtx, sp string, st *ir.Step, b *bindings, m *wmap) error {
	env, err := e.stepEnv(c, sp, st, b, extOf(st.Ext))
	if err != nil {
		return err
	}
	m.set(envKey, env)
	if st.WorkDir != "" {
		d, err := e.text(nil, sp, caps.StepWorkdir, valueText, st.WorkDir)
		if err != nil {
			return err
		}
		m.set(dirKey, str(d))
	}
	return nil
}

// stepLosses reports the step fields Woodpecker has no key for.
func (e *emitter) stepLosses(sp string, st *ir.Step) {
	if st.Timeout != nil {
		e.lose(sp, caps.StepTimeout, "Woodpecker has no step timeout")
	}
	if st.Shell != "" {
		e.lose(sp, caps.StepShell, "Woodpecker runs commands in the image shell, not "+st.Shell)
	}
}

func (e *emitter) pluginStep(c *wfCtx, sp string, st *ir.Step) (*wmap, error) {
	if st.Image == nil || st.Image.Ref == "" {
		e.lose(sp, caps.StepAction, "a plugin step needs an image")
		e.comment("step " + sp + ": plugin without an image")
		return nil, nil
	}
	m := newMap()
	if err := e.common(c, sp, st, m); err != nil {
		return nil, err
	}
	set := newMap()
	for _, k := range sortedKeys(st.With) {
		n, err := e.settingNode(sp, st.With[k])
		if err != nil {
			return nil, err
		}
		set.set(k, n)
	}
	if err := set.mergeMap(extOf(st.Ext)[settKey]); err != nil {
		return nil, err
	}
	if !set.empty() {
		m.set(settKey, set.n)
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

// placeholder is the one step of a workflow that has none Woodpecker can run; Woodpecker rejects a workflow without steps.
func (e *emitter) placeholder(c *wfCtx) (stepDone, error) {
	e.emulate(c.jp, caps.StepScript, "no-op step", "Woodpecker needs a step; no step of the job has a Woodpecker form, so a no-op step stands in")
	e.mark(caps.StepScript, c.jp)
	e.lose(c.jp+"/step/script", caps.StepScript, "the job has no steps; a no-op step keeps the workflow valid")
	m := newMap()
	m.set(nameKey, str(placeholderName))
	img, err := e.stepImage(c, c.jp, &ir.Step{})
	if err != nil {
		return stepDone{}, err
	}
	m.set(imageKey, str(img))
	m.set(cmdsKey, strSeq([]string{"true"}))
	return stepDone{node: m.n, key: placeholderName}, nil
}
