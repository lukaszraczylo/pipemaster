package drone

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/internal/shguard"
)

// GuardMarker ends a guard line so ParseGuard can tell it from a script line that the author wrote.
const GuardMarker = shguard.Marker

// ErrNotGuardable marks a condition that has no POSIX test form.
var ErrNotGuardable = shguard.ErrNotGuardable

// guardDialect is the Drone shell guard: DRONE_* variables, the event compare and the fold option.
func guardDialect(sem expr.Semantics, o options) shguard.Dialect {
	return shguard.Dialect{
		Sem:     sem,
		Name:    func(r expr.Ref) (string, bool) { return VarName(r) },
		Ref:     func(name string) (expr.Ref, bool) { return Ref(name), true },
		Prepare: o.readRefName,
		Compare: func(b expr.Binary) (string, bool, error) {
			if !isEventRef(b.L) && !isEventRef(b.R) {
				return "", false, nil
			}
			t, err := eventCompare(b, sem)
			return t, true, err
		},
		Exact:    func(s string) bool { return o.exact(s, sem) },
		Canon:    canonEventCompare,
		Group:    collapsePush,
		Atom:     atomText,
		AtomRead: atomRead,
	}
}

// Guardable reports whether PrintGuard accepts e under sem.
func Guardable(e expr.Expr, sem expr.Semantics, opts ...Option) bool {
	return guardDialect(sem, optsOf(opts)).Guardable(e)
}

// PrintGuard writes the script line that skips the rest of a step when e is false. The step then shows
// success instead of skipped, so callers must report the guard as an emulation.
//
// sem is the equality semantics of the dialect that wrote e. The shell test is case sensitive, so under a
// folding source (GHA) a comparison is guardable only when its literal has no cased character and the other
// side is a ref; any other folded comparison fails. An event literal maps to its DRONE_BUILD_EVENT value
// (schedule is cron), and one with none, such as push, fails. AllowFold lifts the cased-literal limit.
//
// Literals are written as they are. Drone substitutes ${NAME} in the raw YAML text (research drone.md 6.1),
// so the caller that puts the line in a document must apply Drone `$` escaping and reverse it before
// ParseGuard, which compares the exact text.
func PrintGuard(e expr.Expr, sem expr.Semantics, opts ...Option) (string, error) {
	return guardDialect(sem, optsOf(opts)).Print(e)
}

// ParseGuard inverts PrintGuard. It accepts only text that PrintGuard writes.
func ParseGuard(line string) (expr.Expr, bool) {
	return guardDialect(expr.Semantics{}, options{}).Parse(line)
}

// AddGuard puts the guard for e before the script lines.
func AddGuard(script []string, e expr.Expr, sem expr.Semantics) ([]string, error) {
	return guardDialect(sem, options{}).Add(script, e)
}

// RemoveGuard inverts AddGuard: it strips a leading guard line and returns its condition.
func RemoveGuard(script []string) ([]string, expr.Expr, bool) {
	return guardDialect(expr.Semantics{}, options{}).Remove(script)
}

// eventVar is the shell expansion of the build event.
const eventVar = `"$DRONE_BUILD_EVENT"`

func isEventRef(e expr.Expr) bool {
	r, ok := e.(expr.Ref)
	return ok && slices.Equal(r.Path, keyRefs[KeyEvent].Path)
}

// eventCompare writes a comparison of the event ref with a canonical literal. Canonical push stands for the
// Drone values push and tag, so == is an OR and != an AND of the two tests, in a group of their own. The pair
// needs the ref on the left: ParseGuard reads only that order back.
func eventCompare(b expr.Binary, sem expr.Semantics) (string, error) {
	refLeft := isEventRef(b.L)
	other := b.R
	if !refLeft {
		other = b.L
	}
	lit, ok := other.(expr.Lit)
	if !ok || lit.Kind != expr.LitStr {
		return "", fmt.Errorf("%w: an event ref compares only with a string literal", ErrNotGuardable)
	}
	vals, ok := droneEvents(lit.Str, sem.CaseInsensitiveEq)
	if !ok || len(vals) > 1 && !refLeft {
		return "", fmt.Errorf("%w: event literal %q has no Drone test here", ErrNotGuardable, lit.Str)
	}
	op, join := "=", " || "
	if b.Op == expr.OpNe {
		op, join = "!=", " && "
	}
	tests := make([]string, len(vals))
	for i, v := range vals {
		if refLeft {
			tests[i] = "[ " + eventVar + " " + op + " " + shguard.Quote(v) + " ]"
		} else {
			tests[i] = "[ " + shguard.Quote(v) + " " + op + " " + eventVar + " ]"
		}
	}
	if len(tests) == 1 {
		return tests[0], nil
	}
	return "( " + strings.Join(tests, join) + " )", nil
}

// canonEventCompare rewrites the Drone event literal of a comparison to its canonical name; a literal with
// none, such as push, fails.
func canonEventCompare(b expr.Binary) (expr.Expr, bool) {
	rewrite := func(ref, lit expr.Expr) (expr.Expr, bool) {
		r, isRef := ref.(expr.Ref)
		l, isLit := lit.(expr.Lit)
		if !isRef || !slices.Equal(r.Path, keyRefs[KeyEvent].Path) {
			return lit, true
		}
		if !isLit {
			return nil, false
		}
		if l.Str == "push" {
			return strLit(rawPush), true
		}
		c, ok := canonEvent(l.Str)
		return strLit(c), ok
	}
	r, ok := rewrite(b.L, b.R)
	if !ok {
		return nil, false
	}
	l, ok := rewrite(b.R, b.L)
	if !ok {
		return nil, false
	}
	return expr.Binary{Op: b.Op, L: l, R: r}, true
}

// rawPush marks a Drone push test that is not yet part of a push and tag pair. It holds a NUL, so PrintGuard
// refuses any tree that still has it and ParseGuard fails on a lone push test.
const rawPush = "\x00push"

// collapsePush reads the group that PrintGuard writes for canonical push: push or tag, or neither push nor tag.
func collapsePush(e expr.Expr) expr.Expr {
	b, ok := e.(expr.Binary)
	if !ok {
		return e
	}
	op, join := expr.OpEq, expr.OpOr
	if b.Op == expr.OpAnd {
		op, join = expr.OpNe, expr.OpAnd
	}
	l, lok := b.L.(expr.Binary)
	r, rok := b.R.(expr.Binary)
	if b.Op != join || !lok || !rok || l.Op != op || r.Op != op || !isEventRef(l.L) || !isEventRef(r.L) {
		return e
	}
	ll, lLit := l.R.(expr.Lit)
	rl, rLit := r.R.(expr.Lit)
	if !lLit || !rLit || ll.Str != rawPush || rl.Str != "tag" {
		return e
	}
	return expr.Binary{Op: op, L: l.L, R: strLit(eventPush)}
}
