package concourse

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
)

// runKind says which kind of git version starts the job: a tag version (tag) or a branch push. It is unknown for
// a job with no triggering get, for a pipeline that also follows a time or other resource, and for a Concourse
// source, whose conditions the file already holds as written.
func (jx *jobEmitter) runKind() (tag, known bool) {
	pe := jx.pe
	if pe.e.own || jx.manual {
		return false, false
	}
	if pe.tagSplit {
		return pe.twinPass, true
	}
	var tagged, plain int
	for _, s := range pe.sources {
		if s.typ != typeGit {
			return false, false
		}
		if _, ok := srcOf(s.def)["tag_filter"]; ok {
			tagged++
		} else {
			plain++
		}
	}
	switch {
	case tagged > 0 && plain == 0:
		return true, true
	case plain > 0 && tagged == 0:
		return false, true
	}
	return false, false
}

// foldRun applies what the job knows about the run that starts it to a condition. A verdict other than unknown
// says the condition holds on every such run or on none; otherwise the returned condition is the part that
// still depends on the run.
func (jx *jobEmitter) foldRun(c expr.Expr) (expr.Expr, runVerdict) {
	tag, known := jx.runKind()
	if !known {
		return c, runUnknown
	}
	ev := "push"
	if tag {
		ev = "tag"
	}
	if v := decideRun(c, ev, tag); v != runUnknown {
		return c, v
	}
	f := expr.FoldRun(c, tag)
	if l, ok := f.(expr.Lit); ok {
		return c, verdictOf(expr.LitTruthy(l))
	}
	return f, runUnknown
}

func andGuard(g, c expr.Expr) expr.Expr {
	switch {
	case g == nil:
		return c
	case c == nil:
		return g
	}
	return expr.Binary{Op: expr.OpAnd, L: g, R: c}
}

// foldMark records the condition of a step that the guard no longer spells as written, so the parser restores it.
func (jx *jobEmitter) foldMark(name string, orig, final expr.Expr) {
	if orig == nil || (final != nil && expr.String(final) == expr.String(orig)) {
		return
	}
	a := foldArg{If: expr.String(orig), Fold: true}
	if final != nil {
		a.Guard = expr.String(final)
	}
	jx.pe.e.markArg(caps.StepIf, jx.sm(name), a)
}

// alwaysHolds is true when the event of the run settles the condition as true.
func (jx *jobEmitter) alwaysHolds(c expr.Expr) bool {
	_, v := jx.foldRun(c)
	return v == runAlways
}
