package expr

import (
	"slices"
	"strings"
)

// dispatchSources are the GitLab pipeline sources that start a run through an API call. GHA has one event for them,
// repository_dispatch, and its event_type tells the two apart (the sender writes api or trigger).
var dispatchSources = []string{"api", "trigger"}

const (
	dispatchEvent    = "repository_dispatch"
	dispatchSentinel = "pipemaster-source-"
)

// dispatchSourceVariant states a positive test of the GitLab api or trigger source for a GHA target as a test of
// the repository_dispatch event with that event_type. A negative test has no safe form: a trigger run passes it in
// the source, and the target would reject the same run as a repository_dispatch run, so it stays untranslated.
func dispatchSourceVariant(e Expr, from, to Dialect) (variant, bool) {
	if from.Class != ClassGitLab || to.Class != ClassGHA {
		return variant{}, false
	}
	x, ok := dispatchLeaves(e, false)
	if !ok || x == nil {
		return variant{}, false
	}
	fix := func(t Expr) Expr {
		out, _ := rebuild(t, func(y Expr) (Expr, bool) {
			b, ok := y.(Binary)
			if !ok || b.Op != OpEq {
				return nil, false
			}
			ref, lit, ok := refSide(b.L, b.R)
			src, found := strings.CutPrefix(lit, dispatchSentinel)
			if !ok || !found || pathName(ref) != CtxSHA {
				return nil, false
			}
			return Binary{Op: OpAnd,
				L: Binary{Op: OpEq, L: ctxRef(CtxEvent), R: strLit(dispatchEvent)},
				R: Binary{Op: OpEq, L: Ref{Path: []string{to.ID, "github", "event", "action"}}, R: strLit(src)}}, true
		})
		return out
	}
	return variant{e: x, from: readsAs(from, e), fix: fix}, true
}

func dispatchLeaf(src string) Expr {
	return Binary{Op: OpEq, L: ctxRef(CtxSHA), R: strLit(dispatchSentinel + src)}
}

// dispatchLeaves replaces each positive test of an API source by its sentinel. The result is nil when the condition
// holds none, and not ok when one sits under a negation or a != test.
func dispatchLeaves(e Expr, neg bool) (Expr, bool) {
	switch v := e.(type) {
	case Unary:
		if v.Op != OpNot {
			return nil, true
		}
		x, ok := dispatchLeaves(v.X, !neg)
		if x == nil {
			return nil, ok
		}
		return Unary{Op: v.Op, X: x}, ok
	case Binary:
		switch v.Op {
		case OpAnd, OpOr:
			l, lok := dispatchLeaves(v.L, neg)
			r, rok := dispatchLeaves(v.R, neg)
			if !lok || !rok {
				return nil, false
			}
			if l == nil && r == nil {
				return nil, true
			}
			if l == nil {
				l = v.L
			}
			if r == nil {
				r = v.R
			}
			return Binary{Op: v.Op, L: l, R: r}, true
		case OpEq, OpNe:
			ref, lit, ok := refSide(v.L, v.R)
			if !ok || pathName(ref) != CtxEvent || !slices.Contains(dispatchSources, lit) {
				return nil, true
			}
			if neg || v.Op == OpNe {
				return nil, false
			}
			return dispatchLeaf(lit), true
		}
	}
	return nil, true
}
