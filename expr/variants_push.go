package expr

import (
	"maps"
	"slices"
	"strings"
)

// The sentinels stand in a variant for the target events of a push, and of a promotion or rollback.
const (
	pushSentinel   = "pipemaster-push-event"
	deploySentinel = "pipemaster-deploy-event"
)

func sentinelLeaf(name string) Expr { return Binary{Op: OpEq, L: ctxRef(CtxSHA), R: strLit(name)} }

// isSentinel is the sentinel test as the target states it: an equality, or an exact pattern test.
func isSentinel(e Expr, name string) bool {
	b, ok := e.(Binary)
	if !ok || !isRef(b.L, CtxSHA) {
		return false
	}
	switch b.Op {
	case OpEq:
		lit, ok := isStr(b.R)
		return ok && lit == name
	case OpMatch:
		pat, ok := isStr(b.R)
		m, s, parsed := ParsePattern(pat)
		return ok && parsed && m == ModeExact && strings.EqualFold(s, name)
	}
	return false
}

// pushLiteral is the literal of the canonical push event in the dialect: the push event of a source whose
// pipelines cover a tag push as well, or the value Drone spells it with.
func pushLiteral(d Dialect) (string, bool) {
	if v, ok := d.Events[eventPush]; ok {
		return v, true
	}
	return eventPush, d.TagEvent
}

// pushEventsVariant states a test of the push event of a source that calls a tag push a push for a target that
// reports it as its own event (Dialect.PushEvents, Woodpecker). The source push event covers a branch push and a
// tag push, so a positive test becomes the disjunction of the target events, and a test of its complement becomes
// the disjunction of the other events that the source names. A bare complement would admit the events that only the
// target has (release, deployment), which the source never starts. A push test inside a larger negation has no form.
func pushEventsVariant(e Expr, from, to Dialect) (variant, bool) {
	src, has := pushLiteral(from)
	if len(to.PushEvents) == 0 || !to.TagEvent || !has {
		return variant{}, false
	}
	isPush := func(x Expr) (neg, ok bool) {
		b, isBin := x.(Binary)
		if !isBin || (b.Op != OpEq && b.Op != OpNe) {
			return false, false
		}
		ref, lit, refOK := refSide(b.L, b.R)
		if !refOK || pathName(ref) != CtxEvent || !strings.EqualFold(lit, src) {
			return false, false
		}
		return b.Op == OpNe, true
	}
	var others Expr
	for _, c := range slices.Sorted(maps.Keys(from.Events)) {
		if c == eventPush {
			continue
		}
		leaf := Binary{Op: OpEq, L: ctxRef(CtxEvent), R: strLit(from.Events[c])}
		if others == nil {
			others = leaf
		} else {
			others = Binary{Op: OpOr, L: others, R: leaf}
		}
	}
	if len(from.DeployEvents) > 0 && to.DeployEvent != "" {
		if others == nil {
			others = sentinelLeaf(deploySentinel)
		} else {
			others = Binary{Op: OpOr, L: others, R: sentinelLeaf(deploySentinel)}
		}
	}
	var found, bad bool
	var walk func(Expr, bool) Expr
	walk = func(x Expr, neg bool) Expr {
		switch v := x.(type) {
		case Unary:
			if v.Op != OpNot {
				return x
			}
			if n, ok := isPush(v.X); ok && !n && !neg {
				found = true
				return others
			}
			return Unary{Op: v.Op, X: walk(v.X, !neg)}
		case Binary:
			if v.Op == OpAnd || v.Op == OpOr {
				return Binary{Op: v.Op, L: walk(v.L, neg), R: walk(v.R, neg)}
			}
			n, ok := isPush(x)
			if !ok {
				return x
			}
			switch {
			case neg:
				bad = true
			case n:
				found = true
				return others
			case from.TagEvent:
				// A Drone push test carries the branch push the source names; the condition keeps its own form.
				bad = true
			default:
				found = true
				return sentinelLeaf(pushSentinel)
			}
		}
		return x
	}
	x := walk(e, false)
	if !found || bad || (others == nil && containsNil(x)) {
		return variant{}, false
	}
	fix := func(t Expr) Expr {
		out, _ := rebuild(t, func(y Expr) (Expr, bool) {
			if isSentinel(y, deploySentinel) {
				return Binary{Op: OpEq, L: ctxRef(CtxEvent), R: strLit(to.DeployEvent)}, true
			}
			if !isSentinel(y, pushSentinel) {
				return nil, false
			}
			var or Expr
			for _, ev := range to.PushEvents {
				leaf := Binary{Op: OpEq, L: ctxRef(CtxEvent), R: strLit(ev)}
				if or == nil {
					or = leaf
				} else {
					or = Binary{Op: OpOr, L: or, R: leaf}
				}
			}
			return or, true
		})
		return out
	}
	return variant{e: x, from: readsAs(from, e), fix: fix}, true
}

// containsNil reports a condition that lost a branch to an empty replacement.
func containsNil(e Expr) bool {
	switch v := e.(type) {
	case nil:
		return true
	case Unary:
		return containsNil(v.X)
	case Binary:
		return containsNil(v.L) || containsNil(v.R)
	}
	return false
}
