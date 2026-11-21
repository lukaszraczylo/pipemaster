package tekton

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	shebang       = "#!"
	onErrContinue = "continue"
)

// steps reads the step list in order.
func (b *jobBuilder) steps(v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("steps must be a list")
	}
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return fmt.Errorf("step %d must be a mapping", i+1)
		}
		st, err := b.step(m, i+1)
		if err != nil {
			return err
		}
		b.job.Steps = append(b.job.Steps, st)
	}
	return nil
}

func (b *jobBuilder) step(m *omap, n int) (*ir.Step, error) {
	r := newReader(m)
	name, _ := textOf(r.take("name"))
	id := firstOf(m.marks[""], m.marks["name"], name, fmt.Sprintf("%s-step-%d", b.job.ID, n))
	k := newSink(b.c.p.rep, b.c.file, b.path+"/step/"+id)
	st := &ir.Step{ID: id}
	if t, ok := textOf(r.take("displayName")); ok {
		st.Name = t
	}
	if t, ok := textOf(r.take("image")); ok {
		if img, err := b.tx.template(t); err != nil {
			return nil, err
		} else if img != b.containerRef {
			st.Image = &ir.Image{Ref: img}
		}
	}
	if err := b.command(st, k, r); err != nil {
		return nil, fmt.Errorf("step %s: %w", id, err)
	}
	if v, ok := r.take("workingDir"); ok {
		t, isText := text(v)
		if !isText {
			return nil, fmt.Errorf("step %s: workingDir must be a string", id)
		}
		var err error
		if st.WorkDir, err = b.tx.template(t); err != nil {
			return nil, err
		}
	}
	if v, ok := r.take("timeout"); ok {
		if d, ok := parseDuration(v); ok {
			st.Timeout = &d
		} else {
			k.keep("timeout", v, caps.StepTimeout, "timeout has no positive Go duration; kept in Ext", r.line("timeout"))
		}
	}
	if v, ok := r.take("onError"); ok {
		if t, _ := text(v); t == onErrContinue {
			st.ContinueOnError = true
		} else {
			k.keep("onError", v, caps.StepContinueOnError, "onError other than continue has no IR form; kept in Ext", r.line("onError"))
		}
	}
	if v, ok := r.take("when"); ok {
		entries, err := whenEntries(v)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", id, err)
		}
		st.If = andExpr(st.If, b.condition(entries, r.line("when"), false))
	}
	k.keepRest(r)
	st.Ext = k.ext()
	return st, nil
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func andExpr(l, r expr.Expr) expr.Expr {
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	return expr.Binary{Op: expr.OpAnd, L: l, R: r}
}

// command reads script, command and args, or the StepAction ref.
func (b *jobBuilder) command(st *ir.Step, k *sink, r *reader) error {
	if v, ok := r.take("ref"); ok {
		st.Kind = ir.StepAction
		k.keep("ref", v, caps.StepAction, "a StepAction ref has no IR form; kept in Ext", r.line("ref"))
		if p, ok := r.take("params"); ok {
			k.keep("params", p, caps.StepAction, "the params of a StepAction have no IR field; kept in Ext", r.line("params"))
		}
		return b.env(st, k, r)
	}
	st.Kind = ir.StepScript
	script, hasScript := r.take("script")
	cmd, hasCmd := r.take("command")
	args, hasArgs := r.take("args")
	if hasScript && (hasCmd || hasArgs) {
		return fmt.Errorf("script cannot be combined with command or args")
	}
	var guardEnv []exprtekton.EnvVar
	switch {
	case hasScript:
		t, ok := text(script)
		if !ok {
			return fmt.Errorf("script must be a string")
		}
		var err error
		if guardEnv, err = b.script(st, t); err != nil {
			return err
		}
	case hasCmd || hasArgs:
		if err := b.commandLine(st, k, cmd, args); err != nil {
			return err
		}
	}
	if err := b.envWithout(st, k, r, guardEnv); err != nil {
		return err
	}
	return nil
}

func (b *jobBuilder) env(st *ir.Step, k *sink, r *reader) error { return b.envWithout(st, k, r, nil) }

// script reads script text: a shebang becomes the shell, a leading guard becomes the step condition, and the
// rest becomes script lines. It returns the env entries that only the guard reads.
func (b *jobBuilder) script(st *ir.Step, t string) ([]exprtekton.EnvVar, error) {
	lines := strings.Split(strings.TrimSuffix(t, "\n"), "\n")
	if len(lines) > 0 && strings.HasPrefix(lines[0], shebang) {
		st.Shell = strings.TrimSpace(strings.TrimPrefix(lines[0], shebang))
		lines = lines[1:]
	}
	var guardEnv []exprtekton.EnvVar
	if rest, cond, ok := exprtekton.RemoveGuard(lines); ok {
		st.If = cond
		lines = rest
		guardEnv, _ = exprtekton.GuardEnv(cond)
	}
	for _, l := range lines {
		tl, err := b.tx.template(l)
		if err != nil {
			return nil, err
		}
		st.Script = append(st.Script, tl)
	}
	return guardEnv, nil
}

// commandLine writes command and args as one script line and keeps both lists in Ext, so the emitter can write
// them back while the line is unchanged.
func (b *jobBuilder) commandLine(st *ir.Step, k *sink, cmd, args any) error {
	var words []string
	for _, part := range []any{cmd, args} {
		if part == nil {
			continue
		}
		l, ok := textList(part)
		if !ok {
			return fmt.Errorf("command and args must be lists of strings")
		}
		for _, w := range l {
			q, err := b.quote(w)
			if err != nil {
				return err
			}
			words = append(words, q)
		}
	}
	st.Script = []string{strings.Join(words, " ")}
	k.vals[ExtCommand] = plain(cmd)
	k.vals[ExtArgs] = plain(args)
	k.note(caps.StepScript, "command and args are written as one script line; both lists are kept in Ext", 0)
	return nil
}

const safeWord = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"

// quote writes one command word for a shell. A word that holds a reference is double quoted so its value
// stays one word.
func (b *jobBuilder) quote(w string) (string, error) {
	parts := b.tx.parts(w)
	if slices.ContainsFunc(parts, func(p expr.Part) bool { return p.Expr != nil }) {
		for i, p := range parts {
			if p.Expr == nil {
				parts[i].Text = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(p.Text)
			}
		}
		t, err := expr.PrintScriptTemplate(parts)
		return `"` + t + `"`, err
	}
	var lit strings.Builder
	for _, p := range parts {
		lit.WriteString(p.Text)
	}
	s := lit.String()
	if s != "" && strings.Trim(s, safeWord) == "" {
		return b.escapeText(s)
	}
	return b.escapeText("'" + strings.ReplaceAll(s, "'", `'\''`) + "'")
}

func (b *jobBuilder) escapeText(s string) (string, error) {
	return expr.PrintScriptTemplate([]expr.Part{{Text: s}})
}

// envWithout reads the step env. An entry that only a guard reads is dropped; valueFrom entries stay in Ext.
func (b *jobBuilder) envWithout(st *ir.Step, k *sink, r *reader, skip []exprtekton.EnvVar) error {
	v, ok := r.take("env")
	if !ok {
		return nil
	}
	vars, from, err := b.envList(v, skip)
	if err != nil {
		return err
	}
	st.Env = vars
	if len(from) > 0 {
		k.keep("valueFrom", from, caps.StepEnv, "env entries with valueFrom have no Value form; kept in Ext", r.line("env"))
	}
	return nil
}

// envList reads a Kubernetes env list.
func (b *jobBuilder) envList(v any, skip []exprtekton.EnvVar) ([]ir.Var, map[string]any, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("env must be a list")
	}
	var vars []ir.Var
	from := map[string]any{}
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("an env entry needs a name")
		}
		raw, hasValue := text(m.vals["value"])
		if vf, ok := m.vals["valueFrom"]; ok {
			from[name] = plain(vf)
			continue
		}
		if !hasValue {
			raw = ""
		}
		if slices.ContainsFunc(skip, func(g exprtekton.EnvVar) bool { return g.Name == name && g.Value == raw }) {
			continue
		}
		vars = append(vars, ir.Var{Name: name, Value: b.tx.value(raw)})
	}
	return vars, from, nil
}

// stepTemplate reads the defaults of every step: the image is the job container and the env is the job env.
func (b *jobBuilder) stepTemplate(v any, line int) error {
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("stepTemplate must be a mapping")
	}
	r := newReader(m)
	if t, ok := textOf(r.take("image")); ok {
		ref, err := b.tx.template(t)
		if err != nil {
			return err
		}
		b.job.Container = &ir.Image{Ref: ref}
		b.containerRef = ref
	}
	if ev, ok := r.take("env"); ok {
		vars, from, err := b.envList(ev, nil)
		if err != nil {
			return err
		}
		b.job.Env = vars
		if len(from) > 0 {
			b.k.keep("valueFrom", from, caps.JobEnv, "stepTemplate env entries with valueFrom have no Value form; kept in Ext", r.line("env"))
		}
	}
	if rest := r.rest(); len(rest) > 0 {
		b.k.keep("stepTemplate", m.subset(rest), caps.ExtPassthrough, "stepTemplate."+strings.Join(rest, ", stepTemplate.")+" has no IR field; kept in Ext", line)
	}
	return nil
}

// sidecars maps each sidecar to a service; its other keys stay in Ext.
func (b *jobBuilder) sidecars(v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("sidecars must be a list")
	}
	extra := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return fmt.Errorf("a sidecar needs a name")
		}
		r := newReader(m)
		r.take("name")
		sv := ir.Service{Name: name}
		if t, ok := textOf(r.take("image")); ok {
			sv.Image.Ref = t
		}
		if ev, ok := r.take("env"); ok {
			vars, from, err := b.envList(ev, nil)
			if err != nil {
				return err
			}
			sv.Env = vars
			if len(from) > 0 {
				r.release("env")
			}
		}
		if pv, ok := r.take("ports"); ok {
			ports, whole := containerPorts(pv)
			sv.Ports = ports
			if !whole {
				r.release("ports")
			}
		}
		b.job.Services = append(b.job.Services, sv)
		if rest := r.rest(); len(rest) > 0 {
			extra.set(name, m.subset(rest), m.lines[name])
			b.k.note(caps.JobServices, "sidecar "+name+": key "+rest[0]+" has no IR field; kept in Ext", m.lines[name])
		}
	}
	if len(extra.keys) > 0 {
		b.k.vals["sidecars"] = plain(extra)
	}
	return nil
}

// containerPorts reads ports with only a containerPort; whole is false when another key would be lost.
func containerPorts(v any) (ports []string, whole bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	for _, e := range list {
		m, ok := asMap(e)
		if !ok || len(m.keys) != 1 || m.keys[0] != "containerPort" {
			return nil, false
		}
		t, ok := text(m.vals["containerPort"])
		if !ok {
			return nil, false
		}
		ports = append(ports, t)
	}
	return ports, true
}

// results maps the Task results to outputs. The other result keys stay in Ext.
func (b *jobBuilder) results(v any, line int) {
	list, _ := v.([]any)
	whole := true
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			whole = false
			continue
		}
		b.job.Outputs = append(b.job.Outputs, ir.Output{Name: name})
		whole = whole && len(m.keys) == 1
	}
	if !whole {
		b.k.keep("results", v, caps.JobOutputs, "result keys beside name have no IR field; kept in Ext", line)
	}
}
