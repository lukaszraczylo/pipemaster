package argo

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprargo "github.com/lukaszraczylo/pipemaster/expr/argo"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
)

const (
	statusSucceeded   = "Succeeded"
	statusFailed      = "Failed"
	workflowStatusRef = "workflow.status"
)

// resultAtom maps an IR job result to the depends atom.
var resultAtom = map[string]string{"success": atomSucceeded, "failure": atomFailed, "skipped": atomSkipped}

// taskCond is how the condition of a job is written: `when` text, `depends` text, or the fail-safe.
type taskCond struct {
	when    string
	depends string
	// deps lists the task names of the needs, for `dependencies`.
	deps []string
	// disabled is true when the condition has no form, so the task never runs.
	disabled bool
}

func conjuncts(e expr.Expr) []expr.Expr {
	if b, ok := e.(expr.Binary); ok && b.Op == expr.OpAnd {
		return append(conjuncts(b.L), conjuncts(b.R)...)
	}
	return []expr.Expr{e}
}

// walkExpr calls f on every node of e.
func walkExpr(e expr.Expr, f func(expr.Expr)) {
	f(e)
	switch v := e.(type) {
	case expr.Unary:
		walkExpr(v.X, f)
	case expr.Binary:
		walkExpr(v.L, f)
		walkExpr(v.R, f)
	case expr.Call:
		for _, a := range v.Args {
			walkExpr(a, f)
		}
	}
}

// onlyResults is true when every leaf of e is a needs result ref or a literal.
func onlyResults(e expr.Expr) bool {
	ok, found := true, false
	walkExpr(e, func(n expr.Expr) {
		switch v := n.(type) {
		case expr.Ref:
			if len(v.Path) == 3 && v.Path[0] == expr.PrefixNeeds && v.Path[2] == "result" {
				found = true
			} else {
				ok = false
			}
		case expr.Call, expr.Opaque:
			ok = false
		}
	})
	return ok && found
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

// rewrite maps the references of e to the references that the printer spells in the task scope.
func (tc *jobCtx) rewrite(e expr.Expr) (expr.Expr, bool) {
	return mapRefs(e, func(r expr.Ref) (expr.Expr, bool) {
		if p, ok := ctxparam.Param(r.Path); ok {
			tc.ctxParam(p)
			return r, true
		}
		switch {
		case len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs:
			pname, ok := tc.pp.inputs[r.Path[1]]
			return expr.Ref{Path: []string{expr.PrefixInputs, pname}}, ok
		case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix:
			return r, tc.matrix[r.Path[1]]
		case len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs":
			jj, ok := tc.pp.byID[r.Path[1]]
			if !ok || (!tc.jp.finally && jj.finally) {
				return nil, false
			}
			tc.needed[r.Path[1]] = true
			return expr.Ref{Path: []string{expr.PrefixNeeds, jj.task, "outputs", r.Path[3]}}, true
		}
		return nil, false
	})
}

// whenBody prints e as the body of an expression tag.
func whenBody(e expr.Expr) (string, bool) {
	w, ok := exprargo.ToWhen(e, exprargo.ScopeTask)
	if !ok {
		return "", false
	}
	w = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(w, "{{="), "}}"))
	return w, w != ""
}

// statusBody writes success() and failure() in an exit handler as a test of the workflow status.
func statusBody(c expr.Expr) (body string, handled, ok bool) {
	neg := false
	if u, isNot := c.(expr.Unary); isNot && u.Op == expr.OpNot {
		neg, c = true, u.X
	}
	call, isCall := c.(expr.Call)
	if !isCall || len(call.Args) != 0 {
		return "", false, false
	}
	op := "=="
	if neg {
		op = "!="
	}
	switch call.Name {
	case "always":
		return "", true, !neg
	case "success":
		return workflowStatusRef + " " + op + " '" + statusSucceeded + "'", true, true
	case "failure":
		return workflowStatusRef + " " + op + " '" + statusFailed + "'", true, true
	}
	return "", false, false
}

// dependsText writes an expression over needs results as a depends expression.
func (tc *jobCtx) dependsText(e expr.Expr) (string, bool) {
	switch v := e.(type) {
	case expr.Unary:
		x, ok := tc.dependsText(v.X)
		return "!(" + x + ")", ok && v.Op == expr.OpNot
	case expr.Binary:
		if v.Op == expr.OpAnd || v.Op == expr.OpOr {
			l, ok1 := tc.dependsText(v.L)
			r, ok2 := tc.dependsText(v.R)
			return "(" + l + " " + v.Op + " " + r + ")", ok1 && ok2
		}
		ref, isRef := v.L.(expr.Ref)
		lit, isLit := v.R.(expr.Lit)
		if !isRef || !isLit || lit.Kind != expr.LitStr || (v.Op != expr.OpEq && v.Op != expr.OpNe) {
			return "", false
		}
		jj, known := tc.pp.byID[ref.Path[1]]
		atom := resultAtom[lit.Str]
		if !known || atom == "" || (!tc.jp.finally && jj.finally) {
			return "", false
		}
		s := jj.task + "." + atom
		if v.Op == expr.OpNe {
			s = "!(" + s + ")"
		}
		return s, true
	}
	return "", false
}

// condition decides how the job condition is written. A condition with no form disables the task, because an
// untranslatable condition never runs unguarded.
func (tc *jobCtx) condition() taskCond {
	j, path := tc.jp.j, tc.jp.path
	var c taskCond
	for _, n := range j.Needs {
		if jj, ok := tc.pp.byID[n.JobID]; ok {
			c.deps = append(c.deps, jj.task)
		}
	}
	if j.If == nil {
		return c
	}
	if cc, ok := tc.changesCond(c); ok {
		return cc
	}
	fail := func(detail string) taskCond {
		tc.e.lose(path, caps.JobIf, detail)
		c.disabled = true
		return c
	}
	var whens, depends []string
	mentioned := map[string]bool{}
	opaqueWhen := ""
	written := expr.RefTypeForm(j.If)
	for _, cj := range conjuncts(written) {
		if o, isOpaque := cj.(expr.Opaque); isOpaque {
			text, ok := exprargo.OpaqueWhen(o)
			if !ok {
				return fail("the condition is " + o.Dialect + " text, which Argo cannot read; the task is disabled with when {{= false }}")
			}
			opaqueWhen = text
			continue
		}
		if body, handled, ok := statusBody(cj); handled {
			switch {
			case !ok:
			case !tc.jp.finally:
				return fail("a status function has no form outside the exit handler; the task is disabled with when {{= false }}")
			case body != "":
				whens = append(whens, body)
			}
			continue
		}
		if onlyResults(cj) {
			s, ok := tc.dependsText(cj)
			if !ok {
				return fail("a condition on the result of a job has no depends form; the task is disabled with when {{= false }}")
			}
			depends = append(depends, s)
			walkExpr(cj, func(n expr.Expr) {
				if r, ok := n.(expr.Ref); ok {
					mentioned[r.Path[1]] = true
				}
			})
			continue
		}
		re, ok := tc.rewrite(cj)
		if !ok {
			return fail("a reference of the condition has no Argo spelling; the task is disabled with when {{= false }}")
		}
		body, ok := whenBody(re)
		if !ok {
			return fail("the condition has no when form; the task is disabled with when {{= false }}")
		}
		whens = append(whens, body)
	}
	switch {
	case opaqueWhen != "" && len(whens) > 0:
		return fail("an Argo when text cannot join other conditions; the task is disabled with when {{= false }}")
	case opaqueWhen != "":
		c.when = opaqueWhen
	case len(whens) == 1:
		c.when = "{{= " + whens[0] + " }}"
	case len(whens) > 1:
		c.when = "{{= (" + strings.Join(whens, ") && (") + ") }}"
	}
	if c.when != "" && !c.disabled {
		tc.noteRefType(j.If, written)
	}
	if len(depends) > 0 {
		return tc.withDepends(c, depends, mentioned)
	}
	return c
}

// withDepends adds the depends expression. continueOn does not combine with depends in Argo, so a need with
// allow_failure leaves no form for a condition on a result.
func (tc *jobCtx) withDepends(c taskCond, depends []string, mentioned map[string]bool) taskCond {
	path := tc.jp.path
	for _, n := range tc.jp.j.Needs {
		if jj, ok := tc.pp.byID[n.JobID]; ok && jj.j.AllowFailure {
			tc.e.lose(path, caps.JobIf, "need "+n.JobID+" has allow_failure, which is continueOn, and continueOn does not combine with depends; the task is disabled with when {{= false }}")
			c.disabled = true
			return c
		}
	}
	all := depends
	for _, n := range tc.jp.j.Needs {
		if jj, ok := tc.pp.byID[n.JobID]; ok && !mentioned[n.JobID] {
			all = append(all, jj.task+"."+atomSucceeded)
		}
	}
	c.depends = strings.Join(all, " && ")
	c.deps = nil
	return c
}

func disabledWhen() string { return exprargo.DisabledWhen() }
