package tekton

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
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
		if b.ws != nil && isCopyStep(m) {
			continue
		}
		st, err := b.step(m, i+1)
		if err != nil {
			return err
		}
		if b.prints == nil {
			b.prints = map[string]string{}
		}
		b.prints[st.ID] = printValue(m)
		b.job.Steps = append(b.job.Steps, st)
	}
	return nil
}

func (b *jobBuilder) step(m *omap, n int) (*ir.Step, error) {
	r := newReader(m)
	name, _ := textOf(r.Take("name"))
	id := firstOf(m.Marks[""], m.Marks["name"], name, fmt.Sprintf("%s-step-%d", b.job.ID, n))
	k := newSink(b.c.p.rep, b.c.file, b.path+"/step/"+id)
	st := &ir.Step{ID: id}
	if t, ok := textOf(r.Take("displayName")); ok {
		st.Name = t
	}
	if t, ok := textOf(r.Take("image")); ok {
		if img, err := b.tx.template(t); err != nil {
			return nil, err
		} else if img != b.containerRef {
			st.Image = &ir.Image{Ref: img}
		}
	}
	if err := b.command(st, k, r); err != nil {
		return nil, fmt.Errorf("step %s: %w", id, err)
	}
	if v, ok := r.Take("workingDir"); ok {
		t, isText := text(v)
		if !isText {
			return nil, fmt.Errorf("step %s: workingDir must be a string", id)
		}
		var err error
		if st.WorkDir, err = b.tx.template(t); err != nil {
			return nil, err
		}
	}
	if v, ok := r.Take("timeout"); ok {
		if d, ok := parseDuration(v); ok {
			st.Timeout = &d
		} else {
			k.Keep("timeout", v, caps.StepTimeout, "timeout has no positive Go duration; kept in Ext", r.Line("timeout"))
		}
	}
	if v, ok := r.Take("onError"); ok {
		if t, _ := text(v); t == onErrContinue {
			st.ContinueOnError = true
		} else {
			k.Keep("onError", v, caps.StepContinueOnError, "onError other than continue has no IR form; kept in Ext", r.Line("onError"))
		}
	}
	if v, ok := r.Take("when"); ok {
		entries, err := whenEntries(v)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", id, err)
		}
		b.inTask = true
		cond := b.condition(entries, r.Line("when"), false)
		b.inTask = false
		st.If = andExpr(st.If, cond)
	}
	k.KeepRest(r)
	st.Ext = k.Ext()
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
	if v, ok := r.Take("ref"); ok {
		st.Kind = ir.StepAction
		k.Keep("ref", v, caps.StepAction, "a StepAction ref has no IR form; kept in Ext", r.Line("ref"))
		if p, ok := r.Take("params"); ok {
			k.Keep("params", p, caps.StepAction, "the params of a StepAction have no IR field; kept in Ext", r.Line("params"))
		}
		return b.env(st, k, r)
	}
	st.Kind = ir.StepScript
	script, hasScript := r.Take("script")
	cmd, hasCmd := r.Take("command")
	args, hasArgs := r.Take("args")
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
		ctxEnv := ctxEnvOf(r.M.Vals["env"])
		if guardEnv, err = b.script(st, k, ctxEnv.restore(t)); err != nil {
			return err
		}
		guardEnv = append(guardEnv, ctxEnv.vars...)
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
func (b *jobBuilder) script(st *ir.Step, k *sink, t string) ([]exprtekton.EnvVar, error) {
	lines := strings.Split(strings.TrimSuffix(t, "\n"), "\n")
	if pre := bashPreamble(); len(lines) >= len(pre) && slices.Equal(lines[:len(pre)], pre) {
		lines = lines[len(pre):]
		k.Out[ExtBashDefault] = true
	} else if len(lines) > 0 && strings.HasPrefix(lines[0], shebang) {
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
	k.Out[ExtScriptLine] = st.Script[0]
	k.Out[ExtCommand] = plain(cmd)
	k.Out[ExtArgs] = plain(args)
	k.Note(caps.StepScript, "command and args are written as one script line; both lists are kept in Ext", 0)
	return nil
}

func (b *jobBuilder) quote(w string) (string, error) { return emitkit.ShellWord(b.tx.parts(w)) }

// envWithout reads the step env. An entry that only a guard reads is dropped; valueFrom entries stay in Ext.
func (b *jobBuilder) envWithout(st *ir.Step, k *sink, r *reader, skip []exprtekton.EnvVar) error {
	v, ok := r.Take("env")
	if !ok {
		return nil
	}
	vars, from, err := b.envList(v, skip)
	if err != nil {
		return err
	}
	st.Env = vars
	if len(from) > 0 {
		k.Keep("valueFrom", from, caps.StepEnv, "env entries with valueFrom have no Value form; kept in Ext", r.Line("env"))
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
		raw, hasValue := text(m.Vals["value"])
		if vf, ok := m.Vals["valueFrom"]; ok {
			if v, held := storeValue(vf); held {
				vars = append(vars, ir.Var{Name: name, Value: v})
				continue
			}
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

// storeValue reads the Secret or ConfigMap key that the emitter writes for a secret or a variable.
func storeValue(vf any) (ir.Value, bool) {
	m, ok := asMap(vf)
	if !ok || len(m.Order) != 1 {
		return ir.Value{}, false
	}
	store, key := secretStore, ""
	switch m.Order[0] {
	case kindSecretRef:
	case kindConfigRef:
		store = configStore
	default:
		return ir.Value{}, false
	}
	ref, _ := asMap(m.Vals[m.Order[0]])
	name, _ := text(valueOf(ref, "name"))
	key, _ = text(valueOf(ref, "key"))
	if ref == nil || len(ref.Order) != 2 || name != store || key == "" {
		return ir.Value{}, false
	}
	if store == secretStore {
		return ir.Value{Kind: ir.ValueSecret, Secret: key}, true
	}
	return ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixVars, key}}}, true
}

// stepTemplate reads the defaults of every step: the image is the job container and the env is the job env.
func (b *jobBuilder) stepTemplate(v any, line int) error {
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("stepTemplate must be a mapping")
	}
	r := newReader(m)
	if t, ok := textOf(r.Take("image")); ok {
		ref, err := b.tx.template(t)
		if err != nil {
			return err
		}
		b.job.Container = &ir.Image{Ref: ref}
		b.containerRef = ref
	}
	if ev, ok := r.Take("env"); ok {
		vars, from, err := b.envList(ev, nil)
		if err != nil {
			return err
		}
		b.job.Env = vars
		if len(from) > 0 {
			b.k.Keep("valueFrom", from, caps.JobEnv, "stepTemplate env entries with valueFrom have no Value form; kept in Ext", r.Line("env"))
		}
	}
	if rest := r.Rest(); len(rest) > 0 {
		b.k.Keep("stepTemplate", m.Subset(rest), caps.ExtPassthrough, "stepTemplate."+strings.Join(rest, ", stepTemplate.")+" has no IR field; kept in Ext", line)
	}
	return nil
}

// sidecars maps each sidecar to a service; its other keys stay in Ext.
func (b *jobBuilder) sidecars(v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("sidecars must be a list")
	}
	extra := ynode.NewMap()
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return fmt.Errorf("a sidecar needs a name")
		}
		r := newReader(m)
		r.Take("name")
		sv := ir.Service{Name: name}
		if t, ok := textOf(r.Take("image")); ok {
			ref, err := b.tx.template(t)
			if err != nil {
				return err
			}
			sv.Image.Ref = ref
		}
		if ev, ok := r.Take("env"); ok {
			vars, from, err := b.envList(ev, nil)
			if err != nil {
				return err
			}
			sv.Env = vars
			if len(from) > 0 {
				r.Release("env")
			}
		}
		if pv, ok := r.Take("ports"); ok {
			ports, whole := containerPorts(pv)
			sv.Ports = b.servicePorts(name, ports)
			if !whole {
				r.Release("ports")
			}
		}
		b.job.Services = append(b.job.Services, sv)
		if rest := r.Rest(); len(rest) > 0 {
			extra.Set(name, m.Subset(rest), m.Lines[name])
			b.k.Note(caps.JobServices, "sidecar "+name+": key "+rest[0]+" has no IR field; kept in Ext", m.Lines[name])
		}
	}
	if len(extra.Order) > 0 {
		b.k.Out["sidecars"] = plain(extra)
	}
	return nil
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
		whole = whole && len(m.Order) == 1
	}
	if !whole {
		b.k.Keep("results", v, caps.JobOutputs, "result keys beside name have no IR field; kept in Ext", line)
	}
}

// ctxEnvs holds the step env entries that carry a ctx-* param for the script, as the emitter writes them.
type ctxEnvs struct {
	repl *strings.Replacer
	vars []exprtekton.EnvVar
}

// ctxEnvOf finds the env entries whose name and value are the pair that the emitter writes for a context ref.
func ctxEnvOf(v any) ctxEnvs {
	list, _ := v.([]any)
	var out ctxEnvs
	var pairs []string
	for _, e := range list {
		m, ok := asMap(e)
		if !ok {
			continue
		}
		name, _ := text(valueOf(m, "name"))
		val, isText := text(m.Vals["value"])
		ref, ok := exprtekton.EnvRef(name)
		if !ok || !isText {
			continue
		}
		if _, ctx := ctxparam.Param(ref.Path); !ctx {
			continue
		}
		if word, ok := exprtekton.RefWord(ref); ok && word == val {
			pairs = append(pairs, "${"+name+"}", word)
			out.vars = append(out.vars, exprtekton.EnvVar{Name: name, Value: val})
		}
	}
	if len(pairs) > 0 {
		out.repl = strings.NewReplacer(pairs...)
	}
	return out
}

// restore puts the Tekton variable back where the script reads its env name.
func (c ctxEnvs) restore(script string) string {
	if c.repl == nil {
		return script
	}
	return c.repl.Replace(script)
}
