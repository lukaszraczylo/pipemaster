package expr

import (
	"maps"
	"slices"
)

// eventListsRx is the last step for a target that reads an event only as a pattern (Dialect.EventRx, Woodpecker).
// That target has events the source never reports (pull_request_closed, release, deployment, ...), and a test of
// the form `event !~ /^x$/i` passes all of them. Such a test, or a match used under a negation, would run the job
// where the source would not. Every event atom whose truth must then be at most the source's is rewritten:
//   - a complement used positively becomes the disjunction of every other event the source reports;
//   - a match used under an odd number of negations becomes the negation of that disjunction.
//
// A complement used under a negation and a match used positively are already safe. A source event with no target
// spelling drops out of a disjunction, which only narrows it. The rewrite fails when nothing is left or when a
// pattern does not name one source event
// (lean4/Pipemaster/Woodpecker.lean: woodpecker_event_ne_list, woodpecker_event_ne_target_only).
func eventListsRx(t Expr, from, to Dialect) (Expr, bool) {
	if !to.EventRx {
		return t, true
	}
	return eventListsNode(t, from, to, false)
}

func eventListsNode(e Expr, from, to Dialect, neg bool) (Expr, bool) {
	switch v := e.(type) {
	case Unary:
		x, ok := eventListsNode(v.X, from, to, neg != (v.Op == OpNot))
		if inner, isNot := x.(Unary); isNot && v.Op == OpNot && inner.Op == OpNot && eventOnly(inner.X) {
			return inner.X, ok // !!list: both are boolean
		}
		return Unary{Op: v.Op, X: x}, ok
	case Call:
		args := make([]Expr, len(v.Args))
		for i, a := range v.Args {
			var ok bool
			if args[i], ok = eventListsNode(a, from, to, neg); !ok {
				return nil, false
			}
		}
		return Call{Name: v.Name, Args: args}, true
	case Binary:
		if (v.Op == OpMatch || v.Op == OpNoMatch) && isRef(v.L, CtxEvent) {
			return eventAtomList(v, from, to, neg)
		}
		l, ok := eventListsNode(v.L, from, to, neg)
		if !ok {
			return nil, false
		}
		r, ok := eventListsNode(v.R, from, to, neg)
		return Binary{Op: v.Op, L: l, R: r}, ok
	}
	return e, true
}

// eventAtomList rewrites one `event =~ /^w$/i` or `!~` atom; w is a target spelling. An atom used as written
// (a match positively, a complement under a negation) stays.
func eventAtomList(b Binary, from, to Dialect, neg bool) (Expr, bool) {
	match := b.Op == OpMatch
	if match != neg {
		return b, true
	}
	pat, _ := isStr(b.R)
	m, w, ok := ParsePattern(pat)
	if !ok || m != ModeExact {
		return nil, false
	}
	list, ok := eventListExcept(from, to, w)
	if !ok {
		return nil, false
	}
	if match { // a match under a negation: false-side must hold on the target-only events too.
		return Unary{Op: OpNot, X: list}, true
	}
	return list, true
}

// eventListExcept is the disjunction of the target spellings of every source event but the one that the target
// spells w. It fails when w is no source event's spelling or nothing is left.
func eventListExcept(from, to Dialect, w string) (Expr, bool) {
	var out Expr
	found := false
	for _, c := range slices.Sorted(maps.Keys(from.Events)) {
		spelled := to.spell(c)
		if slices.ContainsFunc(spelled, func(s string) bool { return asciiLower(s) == asciiLower(w) }) {
			found = true
			continue
		}
		for _, s := range spelled {
			m := rx(false, Ref{Path: []string{CtxEvent}}, ModeExact, s)
			if out == nil {
				out = m
			} else {
				out = Binary{Op: OpOr, L: out, R: m}
			}
		}
	}
	return out, found && out != nil
}

// spell is the target events that stand for canonical event c.
func (d Dialect) spell(c string) []string {
	if v, ok := d.Events[c]; ok {
		return []string{v}
	}
	if c == eventPush {
		return d.PushEvents
	}
	return nil
}

// eventOnly is true when every ref of e is the event, so e is a boolean test of the event alone.
func eventOnly(e Expr) bool {
	only := true
	walkRefs(e, func(r Ref) bool { only = only && pathName(r) == CtxEvent; return only })
	return only
}
