package drone

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/container"
)

// ExtCommands records how the lines of a step script group into commands, because the IR keeps one script entry
// per line and a command may span lines.
const ExtCommands = "_commands"

// ExtBashDefault marks a step whose command holds the bash-if-present wrapper, so the step has no IR shell.
const ExtBashDefault = "bash_default"

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
		if img, _ := text(m.vals[keyImage]); name == changesStepName && img == emitkit.ChangesImage {
			continue
		}
		st, err := d.step(m, name)
		if err != nil {
			return err
		}
		d.job.Steps = append(d.job.Steps, st)
	}
	dropRootDeps(d.job.Steps)
	return nil
}

// dropRootDeps removes an empty depends_on from the steps when another step names a dependency: the step is a
// root either way, and the emitter writes the empty list on every root.
func dropRootDeps(steps []*ir.Step) {
	named := func(st *ir.Step) bool { return len(listOf2(extOf(st.Ext)[keyDependsOn])) > 0 }
	if !slices.ContainsFunc(steps, named) {
		return
	}
	for _, st := range steps {
		deps, has := extOf(st.Ext)[keyDependsOn]
		if !has || len(listOf2(deps)) > 0 {
			continue
		}
		delete(extOf(st.Ext), keyDependsOn)
		if len(extOf(st.Ext)) == 0 {
			st.Ext = nil
		}
	}
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
		var bash bool
		if st.Script, shapes, bash, err = d.script(id, cmds); err != nil {
			return nil, err
		}
		if bash {
			k.vals[ExtBashDefault] = true
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
func (d *doc) script(id string, v any) (lines []string, shapes []any, bash bool, err error) {
	l, ok := textList(v)
	if !ok {
		return nil, nil, false, fmt.Errorf("step %s: commands must be a string or a list of strings", id)
	}
	var all []any
	spans := false
	for _, c := range l {
		if inner, wrapped := provider.BashUnwrapCommand(esc(provider.BashOrShScript), c); wrapped {
			t, err := d.tx.template(inner)
			if err != nil {
				return nil, nil, false, err
			}
			cl := strings.Split(t, "\n")
			lines = append(lines, cl...)
			all = append(all, map[string]any{"lines": len(cl), "final": false})
			bash = true
			continue
		}
		if _, guard := dexpr.ParseGuard(unescape(c)); guard {
			lines = append(lines, unescape(c))
			all = append(all, map[string]any{"lines": 1, "final": false})
			continue
		}
		t, err := d.tx.template(c)
		if err != nil {
			return nil, nil, false, err
		}
		cl := strings.Split(strings.TrimSuffix(t, "\n"), "\n")
		lines = append(lines, cl...)
		all = append(all, map[string]any{"lines": len(cl), "final": strings.HasSuffix(t, "\n")})
		spans = spans || len(cl) > 1 || strings.HasSuffix(t, "\n")
	}
	if spans {
		shapes = all
	}
	return lines, shapes, bash, nil
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
