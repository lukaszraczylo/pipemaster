package woodpecker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
)

// Ext keys the parser writes under Ext[woodpecker] on a step. ExtDerivedID marks an ID the parser gave to an
// unnamed list step (Woodpecker calls it step-<index>); ExtMapKey holds the map key of a map-form step whose
// explicit name replaced it as the ID.
const (
	ExtDerivedID = "_derived_id"
	ExtMapKey    = "_map_key"
	// ExtCommands records how the lines of a step script group into commands, because the IR keeps one script entry
	// per line and a command may span lines.
	ExtCommands = "_commands"
)

// commandShape is one command of a step: how many script lines it holds and whether its text ends in a newline.
type commandShape struct {
	Lines int  `json:"lines"`
	Final bool `json:"final,omitempty"`
}

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
			derived := name == ""
			if derived {
				name = "step-" + strconv.Itoa(i)
			}
			st, err := w.step(m, name, i, derived, "")
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
			id, mapKey := k, ""
			if name, _ := text(m.vals["name"]); name != "" {
				id, mapKey = name, k
			}
			st, err := w.step(m, id, i, false, mapKey)
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

func (w *wf) step(m *omap, id string, idx int, derived bool, mapKey string) (*ir.Step, error) {
	r := newReader(m)
	r.take("name")
	sp := w.stepPath(id, idx)
	k := newSink(w.rep, w.file, sp)
	st := &ir.Step{ID: id}
	img, err := container.ReadImage(w.decoder(), "step "+id, r.take)
	if err != nil {
		return nil, err
	}
	st.Image = &img
	var cmdShapes []any
	cmds, isScript := r.take("commands")
	if isScript {
		st.Kind = ir.StepScript
		l, ok := textList(cmds)
		if !ok {
			return nil, fmt.Errorf("step %s: commands must be a string or a list of strings", id)
		}
		var shapes []any
		spans := false
		for _, c := range l {
			t, err := w.tx.template(c)
			if err != nil {
				return nil, err
			}
			lines := strings.Split(strings.TrimSuffix(t, "\n"), "\n")
			st.Script = append(st.Script, lines...)
			shapes = append(shapes, map[string]any{"lines": len(lines), "final": strings.HasSuffix(t, "\n")})
			spans = spans || len(lines) > 1 || strings.HasSuffix(t, "\n")
		}
		if spans {
			cmdShapes = shapes
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
	if cmdShapes != nil {
		k.vals[ExtCommands] = cmdShapes
	}
	if derived {
		k.vals[ExtDerivedID] = true
	}
	if mapKey != "" {
		k.vals[ExtMapKey] = mapKey
	}
	st.Ext = k.ext()
	return st, nil
}

func (w *wf) env(st *ir.Step, k *sink, v any, line int) error {
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("line %d: environment must be a mapping", line)
	}
	vars, left := container.Env(w.decoder(), m)
	st.Env = append(st.Env, vars...)
	if len(left) > 0 {
		rest := m.subset(left)
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
	with, left := container.Settings(w.decoder(), m, isScript)
	if with != nil {
		st.With = with
	}
	if len(left) > 0 {
		rest := m.subset(left)
		k.vals["settings"] = plain(rest)
		for _, name := range rest.keys {
			k.note(caps.ExtPassthrough, "settings."+name+" has no Value form; kept in Ext", rest.lines[name])
		}
	}
	return nil
}
