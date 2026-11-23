package concourse

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// checkoutInfo is a first-step checkout that the source get stands for.
type checkoutInfo struct {
	params map[string]any
	step   *ir.Step
	ok     bool
}

// checkoutParams returns the get params of a checkout that is the first step of the job, or nil. The checkout
// itself is the source get, so no task stands for it.
func (jx *jobEmitter) checkoutParams() map[string]any {
	if jx.checkout == nil {
		jx.checkout = &checkoutInfo{}
		main, _ := jx.split()
		if len(main) > 0 && main[0] != nil && main[0].Kind == ir.StepAction && main[0].Action != nil {
			if t, ok := lookupAction(main[0]); ok && t.Kind == actions.KindImplicit && t.Support.Level != caps.Lost && len(t.Unmapped) == 0 {
				jx.checkout = &checkoutInfo{step: main[0], ok: true, params: map[string]any{}}
				if d, err := strconv.Atoi(strOf(t.With["depth"])); err == nil && d > 0 {
					jx.checkout.params["depth"] = d
				}
			}
		}
	}
	if !jx.checkout.ok || len(jx.checkout.params) == 0 {
		return nil
	}
	return jx.checkout.params
}

func lookupAction(st *ir.Step) (actions.Target, bool) {
	if st.Action == nil || st.Action.Kind != ir.ActionRepo {
		return actions.Target{}, false
	}
	with := map[string]string{}
	for k, v := range st.With {
		s, ok := valueCanon(v)
		if !ok {
			continue
		}
		with[k] = s
	}
	return actions.Lookup(*st.Action, dialect, with)
}

// valueCanon renders a value as the canonical text the action registry reads.
func valueCanon(v ir.Value) (string, bool) {
	switch v.Kind {
	case ir.ValueSecret:
		return "${{ secrets." + v.Secret + " }}", true
	case ir.ValueExpr:
		parts := []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
		s, err := expr.PrintScriptTemplate(parts)
		return s, err == nil
	}
	return v.Lit, true
}

func actionRef(a *ir.ActionRef) string {
	if a == nil {
		return "(none)"
	}
	switch a.Kind {
	case ir.ActionDocker:
		return "docker://" + a.Image
	case ir.ActionLocal:
		return a.Path
	}
	s := a.Owner + "/" + a.Repo
	if a.Path != "" {
		s += "/" + a.Path
	}
	if a.Ref != "" {
		s += "@" + a.Ref
	}
	return s
}

// action writes a step that uses an action. Each action step is held by a placeholder task named after the step,
// or by the source get for a first-step checkout, and a marker carries the whole step.
func (c *chain) action(st *ir.Step, path string, at int) []*yaml.Node {
	jx := c.jx
	e := jx.pe.e
	arg := actionArg{}
	if st.Action == nil || st.Action.Kind != ir.ActionRepo {
		e.lose(path, caps.StepAction, "the action "+actionRef(st.Action)+" has no Concourse form; a placeholder task keeps its place")
		e.comment(path, "uses: "+actionRef(st.Action))
		return c.placeholder(st, path, arg)
	}
	t, ok := lookupAction(st)
	if !ok {
		e.lose(path, caps.StepAction, "the action "+actionRef(st.Action)+" has no Concourse mapping; a placeholder task keeps its place, and the marker and a comment keep the reference")
		e.comment(path, "uses: "+actionRef(st.Action))
		return c.placeholder(st, path, arg)
	}
	e.note(path, caps.StepAction, t.Support.Level, t.Support.Strategy, t.Support.Caveat)
	for _, k := range t.Unmapped {
		e.lose(path, caps.StepAction, "the input "+k+" of the action has no Concourse form")
	}
	if t.Support.Level == caps.Lost {
		return c.placeholder(st, path, arg)
	}
	switch t.Kind {
	case actions.KindImage:
		jx.img, arg.Image = t.Image, t.Image
	case actions.KindImplicit:
		if at == 0 && jx.checkout != nil && jx.checkout.ok && jx.checkout.step == st {
			jx.markCheckout(st, path)
			return nil
		}
		e.emulate(path, caps.StepAction, "placeholder task", "the checkout is not the first step, and the source get runs before every task")
	case actions.KindCache:
		if paths, ok := t.With["paths"].([]string); ok {
			before := len(jx.caches)
			jx.addCaches(path, paths)
			if len(jx.caches) == before && len(paths) > 0 {
				e.lose(path, caps.StepAction, "no cache path can be a task cache")
			}
		}
	case actions.KindUpload:
		jx.uploads[strOf(t.With["name"])] = true
		jx.uploads[""] = true
	case actions.KindDownload:
		if !jx.uploads[""] {
			e.external(path, caps.StepAction, "s3 or registry-image resource", "the download reads an artifact that no earlier step of the job wrote; the operator sets up a resource that carries it from the job that uploads it")
		}
	case actions.KindScript:
		cp := *st
		cp.Script, cp.Kind, cp.Action = t.Script, ir.StepScript, nil
		return []*yaml.Node{c.script(&cp, path)}
	}
	return c.placeholder(st, path, arg)
}

// addCaches adds task cache paths that a task directory can hold.
func (jx *jobEmitter) addCaches(path string, paths []string) {
	e := jx.pe.e
	for _, p := range paths {
		clean := strings.TrimPrefix(p, "./")
		if clean == "" || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "~") || strings.Contains(clean, "..") || strings.Contains(clean, varOpen) || strings.Contains(clean, "${{") {
			e.lose(path, caps.StepAction, "the cache path "+p+" is not a relative path inside the task directory")
			continue
		}
		jx.caches = appendOnce(jx.caches, clean)
	}
}

func appendOnce(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

// markCheckout writes the marker of a first-step checkout.
func (jx *jobEmitter) markCheckout(st *ir.Step, path string) {
	body, err := carryYAML(st)
	if err != nil {
		jx.pe.e.lose(path, caps.StepAction, "the action marker cannot be written: "+err.Error())
		return
	}
	var params any
	if p := jx.checkout.params; len(p) > 0 {
		params = p
	}
	jx.pe.e.markArg(caps.StepAction, jx.sm(adapter{}.SanitizeID(st.ID)), actionArg{Step: body, Get: jx.pe.primary.name, Params: canonJSON(params)})
}

// placeholder writes the task that keeps the place of an action that Concourse does not run. The marker holds
// the canonical text of the task, so the parser restores the action only when the file still has it.
func (c *chain) placeholder(st *ir.Step, path string, arg actionArg) []*yaml.Node {
	jx := c.jx
	e := jx.pe.e
	name := jx.stepName(st, path)
	cfg := newMap()
	cfg.SetStr("platform", defaultPlatform)
	cfg.Set("image_resource", imageNode(defaultImage, "", defaultTag))
	run := newMap()
	run.SetStr("path", shellCommand)
	run.Set("args", strSeq([]string{defaultFlags, placeholderCmd}))
	cfg.Set("run", run.N)
	m := newMap()
	m.SetStr("task", name)
	m.Set("config", cfg.N)
	held := *st
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		e.lose(path, caps.StepAction, "the action marker cannot be written: "+err.Error())
		return []*yaml.Node{m.N}
	}
	arg.Step, arg.Task, arg.Hold = body, name, nodeCanon(m.N)
	e.markArg(caps.StepAction, jx.sm(name), arg)
	e.emulate(path, caps.StepAction, "placeholder task", "Concourse does not run the action; a task that runs "+placeholderCmd+" keeps its place")
	return []*yaml.Node{m.N}
}

// withoutBookkeeping drops the Ext keys that a parser sets only to rebuild its own layout.
func withoutBookkeeping(x ir.Ext) ir.Ext {
	var out ir.Ext
	for prov, raw := range x {
		m, ok := raw.(map[string]any)
		if !ok {
			out = setExt(out, prov, raw)
			continue
		}
		kept := map[string]any{}
		for k, v := range m {
			if !emitkit.BookkeepingExt[k] {
				kept[k] = v
			}
		}
		if len(kept) > 0 {
			out = setExt(out, prov, kept)
		}
	}
	return out
}

func setExt(e ir.Ext, k string, v any) ir.Ext {
	if e == nil {
		e = ir.Ext{}
	}
	e[k] = v
	return e
}

// verbatim writes a step that a Concourse file held and the IR cannot run: a get or put, a step kind with no IR
// form, or a task file. Another origin has no such step, so it is lost.
func (c *chain) verbatim(st *ir.Step, path string) *yaml.Node {
	jx := c.jx
	e := jx.pe.e
	ext := extOf(st.Ext)
	switch st.Kind {
	case ir.StepPlugin:
		raw, ok := ext[ExtStep]
		raw = jx.addNestedPassed(raw)
		if !ok {
			e.lose(path, caps.ExtPassthrough, "the plugin step has no verbatim step to write")
			return nil
		}
		jx.useResources(raw)
		return e.node(path, "the step", raw)
	case ir.StepResource:
		m, _ := jx.addNestedPassed(deepCopy(ext)).(map[string]any)
		if st.Timeout != nil {
			m["timeout"] = durationText(st.Timeout)
		}
		jx.useResources(m)
		return orderedNode(m, []string{"get", "put", "resource", "passed", "trigger", "params", "inputs", "get_params"}, nil)
	}
	file := strOf(ext["file"])
	name := jx.stepName(st, path)
	sc := jx.newScope("")
	sc.envParams(path, st.Env)
	m := newMap()
	m.SetStr("task", name)
	m.SetStr("file", file)
	if len(sc.params) > 0 || ext[ExtParams] != nil {
		params := newMap()
		for _, p := range sc.params {
			params.SetStr(p.name, p.value)
		}
		if raw, ok := ext[ExtParams].(map[string]any); ok {
			for _, k := range sortedKeys(raw) {
				params.Set(k, e.node(path, "param "+k, raw[k]))
			}
		}
		m.Set("params", params.N)
	}
	if st.Timeout != nil {
		m.SetStr("timeout", durationText(st.Timeout))
	}
	if err := m.AddExtra(ext, stepSkip); err != nil {
		e.lose(path, caps.ExtPassthrough, "the Ext of the step cannot be written: "+err.Error())
	}
	jx.secretMarker(sc, name)
	return m.N
}

// useResources marks the resources that a verbatim step names as used, and adds the ones that only the Ext defines.
func (jx *jobEmitter) useResources(v any) {
	pe := jx.pe
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			jx.useResources(x)
		}
	case map[string]any:
		name := strOf(t["resource"])
		for _, k := range []string{"get", "put"} {
			if n := strOf(t[k]); n != "" && name == "" {
				name = n
			}
		}
		if name != "" {
			s := pe.byName[name]
			if s == nil {
				s = pe.adopt(name)
			}
			if s != nil {
				s.used = true
			}
		}
		for k, x := range t {
			if k != "get" && k != "put" && k != "resource" {
				jx.useResources(x)
			}
		}
	}
}
