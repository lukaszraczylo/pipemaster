package woodpecker

import (
	"fmt"
	"strconv"

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
	depKey          = "depends_on"
	dirKey          = "directory"
	nameKey         = "name"
	entryKey        = "entrypoint"
	extDerive       = ExtDerivedID
)

// stepDone is one emitted step: the node and the map key it has in map form.
type stepDone struct {
	node *yaml.Node
	key  string
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
		d, err := e.step(c, st, i)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", st.ID, err)
		}
		if d.node == nil {
			continue
		}
		done = append(done, d)
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
		return st.Name
	}
	if st.Name != "" && st.Name != st.ID {
		e.lose(sp, caps.DisplayName, "a Woodpecker step has one name, which carries the id; the display name is kept in round-trip metadata")
	}
	return st.ID
}

func (e *emitter) step(c *wfCtx, st *ir.Step, idx int) (stepDone, error) {
	sp := c.jp + "/step/" + st.ID
	e.foreignExt(sp, st.Ext)
	var m *wmap
	var err error
	switch {
	case st.Action != nil || st.Kind == ir.StepAction || st.Kind == ir.StepPipelineCall:
		m, err = e.actionStep(c, sp, st)
	case st.Kind == ir.StepPlugin:
		m, err = e.pluginStep(c, sp, st)
	case st.Kind == ir.StepScript || len(st.Script) > 0:
		m, err = e.scriptStep(c, sp, st)
	default:
		e.lose(sp, caps.StepAction, "step kind "+string(st.Kind)+" has no Woodpecker form")
		e.comment("step " + sp + " kind " + string(st.Kind))
	}
	if err != nil || m == nil {
		return stepDone{}, err
	}
	return e.finishStep(c, sp, st, idx, m)
}

// finishStep adds the name, the condition and the Ext keys, and orders the keys of the step.
func (e *emitter) finishStep(c *wfCtx, sp string, st *ir.Step, idx int, body *wmap) (stepDone, error) {
	ext := extOf(st.Ext)
	m := newMap()
	key := st.ID
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
		if name != "" {
			m.set(nameKey, str(name))
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
	if raw, ok := ext[keyWhen]; ok {
		n, err := plainNode(raw)
		if err != nil {
			return stepDone{}, err
		}
		m.set(keyWhen, n)
	} else {
		m.set(keyWhen, e.stepWhen(sp, st.If))
	}
	skip := map[string]bool{envKey: true, settKey: true, keyWhen: true}
	return stepDone{node: m.n, key: key}, m.addExtra(ext, skip)
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
		}
		return ref, nil
	}
	return e.text(nil, sp, caps.StepImage, valueText, ref)
}

// stepEnv builds the environment of a step: its own variables, the secrets its text reads, then the job and pipeline variables.
func (e *emitter) stepEnv(c *wfCtx, sp string, st *ir.Step, b *bindings, ext map[string]any) (*yaml.Node, error) {
	env := newMap()
	if err := e.envNode(sp, st.Env, env); err != nil {
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
	}
	return nil
}

func (e *emitter) scriptStep(c *wfCtx, sp string, st *ir.Step) (*wmap, error) {
	b := &bindings{}
	var lines []*yaml.Node
	for _, l := range st.Script {
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
