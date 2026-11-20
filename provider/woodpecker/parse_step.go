package woodpecker

import (
	"fmt"
	"strconv"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	failureIgnore = "ignore"
	failureFail   = "fail"
)

// steps reads the step list or map in order.
func (w *wf) steps(j *ir.Job, v any) error {
	switch t := v.(type) {
	case []any:
		for i, e := range t {
			m, ok := asMap(e)
			if !ok {
				return fmt.Errorf("step %d must be a mapping", i+1)
			}
			name, _ := text(m.vals["name"])
			st, err := w.step(m, name, i, true)
			if err != nil {
				return err
			}
			j.Steps = append(j.Steps, st)
		}
	case *omap:
		for i, k := range t.keys {
			m, ok := asMap(t.vals[k])
			if !ok {
				return fmt.Errorf("line %d: step %s must be a mapping", t.lines[k], k)
			}
			st, err := w.step(m, k, i, false)
			if err != nil {
				return err
			}
			j.Steps = append(j.Steps, st)
		}
	default:
		return fmt.Errorf("steps must be a list or a mapping")
	}
	return nil
}

func (w *wf) stepPath(id string, idx int) string {
	if id == "" {
		id = "#" + strconv.Itoa(idx)
	}
	return w.jobPath() + "/step/" + id
}

func (w *wf) step(m *omap, id string, idx int, nameField bool) (*ir.Step, error) {
	r := newReader(m)
	if nameField {
		r.take("name")
	}
	sp := w.stepPath(id, idx)
	k := newSink(w.rep, w.file, sp)
	st := &ir.Step{ID: id}
	img, ok := text(m.vals["image"])
	if !ok {
		return nil, fmt.Errorf("step %s: image is required", id)
	}
	r.take("image")
	ref, err := w.tx.template(img)
	if err != nil {
		return nil, err
	}
	st.Image = &ir.Image{Ref: ref}
	if v, ok := r.take("entrypoint"); ok {
		l, isList := textList(v)
		if !isList {
			return nil, fmt.Errorf("step %s: entrypoint must be a string or a list", id)
		}
		for _, e := range l {
			t, err := w.tx.template(e)
			if err != nil {
				return nil, err
			}
			st.Image.Entrypoint = append(st.Image.Entrypoint, t)
		}
	}
	cmds, isScript := r.take("commands")
	if isScript {
		st.Kind = ir.StepScript
		l, ok := textList(cmds)
		if !ok {
			return nil, fmt.Errorf("step %s: commands must be a string or a list of strings", id)
		}
		for _, c := range l {
			t, err := w.tx.template(c)
			if err != nil {
				return nil, err
			}
			st.Script = append(st.Script, t)
		}
	} else {
		st.Kind = ir.StepPlugin
	}
	if v, ok := r.take("directory"); ok {
		t, isText := text(v)
		if !isText {
			return nil, fmt.Errorf("step %s: directory must be a string", id)
		}
		if st.WorkDir, err = w.tx.template(t); err != nil {
			return nil, err
		}
	}
	if v, ok := r.take("environment"); ok {
		if err := w.env(st, k, v, r.line("environment")); err != nil {
			return nil, err
		}
	}
	if v, ok := r.take("settings"); ok {
		if err := w.settings(st, k, v, isScript, r.line("settings")); err != nil {
			return nil, err
		}
	}
	if v, ok := r.take("failure"); ok {
		t, _ := text(v)
		switch t {
		case failureIgnore:
			st.ContinueOnError = true
		case failureFail:
		default:
			k.keep("failure", v, caps.StepContinueOnError, "failure: "+t+" has no IR form; kept in Ext", r.line("failure"))
		}
	}
	if v, ok := r.take(whenKey); ok {
		spec, err := parseWhen(v, r.line(whenKey))
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", id, err)
		}
		if st.If, err = spec.gate(k); err != nil {
			return nil, fmt.Errorf("step %s: when: %w", id, err)
		}
	}
	k.keepRest(r, nil)
	st.Ext = k.ext()
	return st, nil
}

// envValue reads an environment or setting value: a scalar, or {from_secret}. ok is false for any other shape.
func (w *wf) envValue(v any) (ir.Value, bool) {
	if t, ok := text(v); ok {
		return w.tx.value(t), true
	}
	if m, ok := asMap(v); ok && len(m.keys) == 1 {
		if name, ok := text(m.vals["from_secret"]); ok {
			return ir.Value{Kind: ir.ValueSecret, Secret: name}, true
		}
	}
	return ir.Value{}, false
}

func (w *wf) env(st *ir.Step, k *sink, v any, line int) error {
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("line %d: environment must be a mapping", line)
	}
	rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for _, name := range m.keys {
		if val, ok := w.envValue(m.vals[name]); ok {
			st.Env = append(st.Env, ir.Var{Name: name, Value: val})
			continue
		}
		rest.set(name, m.vals[name], m.lines[name])
	}
	if len(rest.keys) > 0 {
		k.vals["environment"] = plain(rest)
		for _, name := range rest.keys {
			k.note(caps.StepEnv, "environment."+name+" has no Value form; kept in Ext", rest.lines[name])
		}
	}
	return nil
}

// settings maps scalar and from_secret settings of a plugin step to With. A list or map setting, and every
// setting of a step that also has commands, has no Value form and stays in Ext.
func (w *wf) settings(st *ir.Step, k *sink, v any, isScript bool, line int) error {
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("line %d: settings must be a mapping", line)
	}
	rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for _, name := range m.keys {
		if val, ok := w.envValue(m.vals[name]); ok && !isScript {
			if st.With == nil {
				st.With = map[string]ir.Value{}
			}
			st.With[name] = val
			continue
		}
		rest.set(name, m.vals[name], m.lines[name])
	}
	if len(rest.keys) > 0 {
		k.vals["settings"] = plain(rest)
		for _, name := range rest.keys {
			k.note(caps.ExtPassthrough, "settings."+name+" has no Value form; kept in Ext", rest.lines[name])
		}
	}
	return nil
}
