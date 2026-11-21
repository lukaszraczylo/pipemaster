package tekton

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
)

// scope says where a condition is read: by the Pipeline (a PipelineTask when) or by the Task (a step when or guard).
type scope uint8

const (
	pipelineScope scope = iota
	taskScope
)

// condition is how a condition is written: when entries, a shell guard, or neither (the fail-safe).
type condition struct {
	guard    expr.Expr
	when     []exprtekton.Entry
	disabled bool
}

// mapRefs rewrites every reference of e; ok is false when f refuses one or e holds a node it cannot rewrite.
func mapRefs(e expr.Expr, f func(expr.Ref) (expr.Expr, bool)) (expr.Expr, bool) {
	switch v := e.(type) {
	case expr.Ref:
		return f(v)
	case expr.Lit:
		return v, true
	case expr.Unary:
		x, ok := mapRefs(v.X, f)
		return expr.Unary{Op: v.Op, X: x}, ok
	case expr.Binary:
		l, ok1 := mapRefs(v.L, f)
		r, ok2 := mapRefs(v.R, f)
		return expr.Binary{Op: v.Op, L: l, R: r}, ok1 && ok2
	}
	return nil, false
}

// rewrite maps the references of e to the references whose Tekton word is valid in scope sc.
func (tc *taskCtx) rewrite(e expr.Expr, sc scope) (expr.Expr, bool) {
	return mapRefs(e, func(r expr.Ref) (expr.Expr, bool) {
		if p, ok := exprtekton.CtxParam(r.Path); ok {
			tc.pp.ctx[p] = true
			if sc == taskScope {
				tc.ctxParam(p)
			}
			return r, true
		}
		switch {
		case len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs:
			pname, ok := tc.pp.inputs[r.Path[1]]
			if !ok {
				return nil, false
			}
			if sc == taskScope {
				n, _ := tc.inputParam(r.Path[1])
				pname = n
			}
			return expr.Ref{Path: []string{expr.PrefixInputs, pname}}, true
		case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
			return r, sc == taskScope && tc.matrix[r.Path[1]]
		case len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs":
			if sc == taskScope {
				n, ok := tc.needParam(r.Path[1], r.Path[3])
				return expr.Ref{Path: []string{expr.PrefixInputs, n}}, ok
			}
			jj, ok := tc.pp.byID[r.Path[1]]
			if !ok || !resultNameRE.MatchString(r.Path[3]) || (!tc.jp.finally && jj.finally) {
				return nil, false
			}
			tc.needed[r.Path[1]] = true
			return expr.Ref{Path: []string{expr.PrefixNeeds, jj.tname, "outputs", r.Path[3]}}, true
		}
		return nil, false
	})
}

func conjuncts(e expr.Expr) []expr.Expr {
	if b, ok := e.(expr.Binary); ok && b.Op == expr.OpAnd {
		return append(conjuncts(b.L), conjuncts(b.R)...)
	}
	return []expr.Expr{e}
}

// statusEntry writes the status checks of a finally task as `when` entries that read $(tasks.status) and
// $(tasks.<name>.status). skip is true for a call that adds no entry.
func (tc *taskCtx) statusEntry(c expr.Expr) (en exprtekton.Entry, skip, ok bool) {
	neg := false
	if u, isNot := c.(expr.Unary); isNot && u.Op == expr.OpNot {
		neg, c = true, u.X
	}
	op := exprtekton.OpIn
	if neg {
		op = exprtekton.OpNotIn
	}
	switch v := c.(type) {
	case expr.Call:
		if len(v.Args) != 0 {
			return en, false, false
		}
		switch v.Name {
		case "always":
			return en, !neg, !neg
		case "failure":
			return exprtekton.Entry{Input: "$(tasks.status)", Operator: op, Values: []string{"Failed"}}, false, tc.jp.finally
		case "success":
			if !tc.jp.finally {
				return en, !neg, !neg
			}
			return exprtekton.Entry{Input: "$(tasks.status)", Operator: op, Values: []string{"Succeeded"}}, false, true
		}
	case expr.Binary:
		r, isRef := v.L.(expr.Ref)
		lit, isLit := v.R.(expr.Lit)
		if !isRef || !isLit || neg || len(r.Path) != 3 || r.Path[0] != expr.PrefixNeeds || r.Path[2] != "result" || lit.Kind != expr.LitStr {
			return en, false, false
		}
		jj, known := tc.pp.byID[r.Path[1]]
		status := map[string]string{"failure": "Failed", "success": "Succeeded", "skipped": "None"}[lit.Str]
		if !known || status == "" || !tc.jp.finally || (v.Op != expr.OpEq && v.Op != expr.OpNe) {
			return en, false, false
		}
		op = exprtekton.OpIn
		if v.Op == expr.OpNe {
			op = exprtekton.OpNotIn
		}
		tc.needed[r.Path[1]] = true
		return exprtekton.Entry{Input: "$(tasks." + jj.tname + ".status)", Operator: op, Values: []string{status}}, false, true
	}
	return en, false, false
}

// whenEntries writes e as `when` entries; ok is false when some conjunct has no entry form.
func (tc *taskCtx) whenEntries(e expr.Expr, sc scope) (out []exprtekton.Entry, ok bool) {
	for _, c := range conjuncts(e) {
		if o, isOpaque := c.(expr.Opaque); isOpaque {
			en, good := exprtekton.OpaqueEntry(o)
			if !good || en.CEL != "" {
				return nil, false
			}
			out = append(out, en)
			continue
		}
		if en, skip, good := tc.statusEntry(c); good {
			if !skip {
				out = append(out, en)
			}
			continue
		}
		re, good := tc.rewrite(c, sc)
		if !good {
			return nil, false
		}
		es, good := exprtekton.ToWhen(re)
		if !good {
			return nil, false
		}
		out = append(out, es...)
	}
	return mergeNotIn(out), true
}

// mergeNotIn joins adjacent notin entries on one input, as ToWhen does, so the entries read back the same.
func mergeNotIn(es []exprtekton.Entry) []exprtekton.Entry {
	var out []exprtekton.Entry
	for _, en := range es {
		if n := len(out) - 1; n >= 0 && en.Operator == exprtekton.OpNotIn && out[n].Operator == exprtekton.OpNotIn && out[n].Input == en.Input {
			out[n].Values = append(out[n].Values, en.Values...)
			continue
		}
		out = append(out, en)
	}
	return out
}

// cond decides how e is written. A condition with no `when` form is guarded in the shell when a guard can state
// it, and otherwise disables the task or step, because an untranslatable condition never runs unguarded.
func (tc *taskCtx) cond(path string, f caps.Feature, e expr.Expr, sc scope) condition {
	if e == nil {
		return condition{}
	}
	if f == caps.JobIf && !tc.jp.finally && len(tc.jp.j.Needs) > 0 && callsAlways(e) {
		tc.e.lose(path, f, "always() has no Tekton form outside finally; Tekton skips the task when a dependency fails")
	}
	if en, ok := tc.whenEntries(e, sc); ok {
		return condition{when: en}
	}
	if !expr.HasOpaque(e) {
		if re, ok := tc.rewrite(e, taskScope); ok && exprtekton.Guardable(re) {
			if _, envOK := exprtekton.GuardEnv(re); envOK {
				tc.e.emulate(path, f, "shell guard", "the condition has no when form; the script steps start with a guard line that reads the values from step env")
				return condition{guard: re}
			}
		}
	}
	why := "the condition has no Tekton when form and no shell guard"
	if o := firstOpaque(e); o != nil {
		if en, ok := exprtekton.OpaqueEntry(*o); ok && en.CEL != "" {
			why = "a cel when entry is alpha and off by default"
		}
		tc.e.lose(path, caps.ExprOpaque, "opaque expression "+expr.String(*o)+" has no Tekton form: "+why)
	}
	tc.e.lose(path, f, why+"; the "+map[caps.Feature]string{caps.JobIf: "task", caps.StepIf: "step"}[f]+" never runs")
	return condition{disabled: true, when: exprtekton.DisabledWhen()}
}

func firstOpaque(e expr.Expr) *expr.Opaque {
	switch v := e.(type) {
	case expr.Opaque:
		return &v
	case expr.Unary:
		return firstOpaque(v.X)
	case expr.Binary:
		if o := firstOpaque(v.L); o != nil {
			return o
		}
		return firstOpaque(v.R)
	case expr.Call:
		for _, a := range v.Args {
			if o := firstOpaque(a); o != nil {
				return o
			}
		}
	}
	return nil
}

// guardEnv adds the step env entries that a guard reads.
func guardEnv(sc *stepScope, g expr.Expr) {
	vars, _ := exprtekton.GuardEnv(g)
	for _, v := range vars {
		sc.add(v.Name, nameValue(v.Name, v.Value))
	}
}

// whenNode renders `when` entries. A cel entry never reaches it: whenEntries refuses one.
func whenNode(es []exprtekton.Entry) *yaml.Node {
	out := seqOf()
	for _, en := range es {
		m := newMap()
		m.setStr("input", en.Input)
		m.setStr("operator", en.Operator)
		m.set("values", strSeq(en.Values))
		out.Content = append(out.Content, m.n)
	}
	return out
}

// callsAlways reports whether the condition calls always() outside a negation.
func callsAlways(e expr.Expr) bool {
	switch v := e.(type) {
	case expr.Call:
		return v.Name == "always" && len(v.Args) == 0
	case expr.Binary:
		return callsAlways(v.L) || callsAlways(v.R)
	}
	return false
}
