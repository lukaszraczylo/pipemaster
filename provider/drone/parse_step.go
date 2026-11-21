package drone

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
)

// ExtCommands records how the lines of a step script group into commands, because the IR keeps one script entry
// per line and a command may span lines.
const ExtCommands = "_commands"

const (
	failureIgnore = "ignore"
	keyImage      = "image"
)

// steps reads the step list in order.
func (d *doc) steps(v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("steps must be a list")
	}
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return fmt.Errorf("step %d must be a mapping", i+1)
		}
		name, _ := text(m.vals["name"])
		if name == "" {
			return fmt.Errorf("step %d needs a name", i+1)
		}
		st, err := d.step(m, name)
		if err != nil {
			return err
		}
		d.job.Steps = append(d.job.Steps, st)
	}
	return nil
}

func (d *doc) step(m *omap, id string) (*ir.Step, error) {
	r := newReader(m)
	r.take("name")
	k := newSink(d.rep, d.file, d.jobPath()+"/step/"+id, d.base)
	st := &ir.Step{ID: id}
	if _, has := m.vals[keyImage]; has || d.docker {
		img, err := container.ReadImage(d.decoder(), "step "+id, r.take)
		if err != nil {
			return nil, err
		}
		st.Image = &img
	}
	cmds, isScript := r.take("commands")
	var shapes []any
	if isScript {
		st.Kind = ir.StepScript
		var err error
		if st.Script, shapes, err = d.script(id, cmds); err != nil {
			return nil, err
		}
	} else {
		st.Kind = ir.StepPlugin
	}
	if v, ok := r.take("working_dir"); ok {
		t, isText := text(v)
		if !isText {
			return nil, fmt.Errorf("step %s: working_dir must be a string", id)
		}
		var err error
		if st.WorkDir, err = d.tx.template(t); err != nil {
			return nil, err
		}
	}
	if err := d.stepMaps(st, k, r, isScript); err != nil {
		return nil, err
	}
	if v, ok := r.take("failure"); ok {
		if t, _ := text(v); t == failureIgnore {
			st.ContinueOnError = true
		} else {
			k.keep("failure", v, caps.StepContinueOnError, "failure: "+t+" has no IR form; kept in Ext", r.line("failure"))
		}
	}
	if v, ok := r.take(keyWhen); ok {
		cond, err := d.condition(k, keyWhen, v, r.line(keyWhen))
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", id, err)
		}
		st.If = cond
	}
	k.keepRest(r, nil)
	if shapes != nil {
		k.vals[ExtCommands] = shapes
	}
	st.Ext = k.ext()
	return st, nil
}

// script reads commands as canonical script lines. shapes is non-nil when a command spans lines or ends in a
// newline.
func (d *doc) script(id string, v any) (lines []string, shapes []any, err error) {
	l, ok := textList(v)
	if !ok {
		return nil, nil, fmt.Errorf("step %s: commands must be a string or a list of strings", id)
	}
	var all []any
	spans := false
	for _, c := range l {
		t, err := d.tx.template(c)
		if err != nil {
			return nil, nil, err
		}
		cl := strings.Split(strings.TrimSuffix(t, "\n"), "\n")
		lines = append(lines, cl...)
		all = append(all, map[string]any{"lines": len(cl), "final": strings.HasSuffix(t, "\n")})
		spans = spans || len(cl) > 1 || strings.HasSuffix(t, "\n")
	}
	if spans {
		shapes = all
	}
	return lines, shapes, nil
}

// stepMaps reads environment and settings.
func (d *doc) stepMaps(st *ir.Step, k *sink, r *reader, isScript bool) error {
	if v, ok := r.take("environment"); ok {
		m, isMap := asMap(v)
		if !isMap {
			return fmt.Errorf("line %d: environment must be a mapping", r.line("environment"))
		}
		vars, left := container.Env(d.decoder(), m)
		st.Env = append(st.Env, vars...)
		d.keepLeft(k, "environment", caps.StepEnv, m, left)
	}
	if v, ok := r.take("settings"); ok {
		m, isMap := asMap(v)
		if !isMap {
			return fmt.Errorf("line %d: settings must be a mapping", r.line("settings"))
		}
		with, left := container.Settings(d.decoder(), m, isScript)
		if with != nil {
			st.With = with
		}
		d.keepLeft(k, "settings", caps.ExtPassthrough, m, left)
	}
	return nil
}

// keepLeft keeps the keys of a mapping that have no Value form in Ext.
func (d *doc) keepLeft(k *sink, key string, f caps.Feature, m *omap, left []string) {
	if len(left) == 0 {
		return
	}
	rest := m.subset(left)
	k.vals[key] = plain(rest)
	for _, name := range rest.keys {
		k.note(f, key+"."+name+" has no Value form; kept in Ext", rest.lines[name])
	}
}
