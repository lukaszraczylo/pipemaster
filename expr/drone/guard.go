package drone

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// GuardMarker ends a guard line so ParseGuard can tell it from a script line that the author wrote.
const GuardMarker = "# pipemaster:guard"

const (
	guardOpen  = "if ! "
	guardClose = "; then exit 0; fi " + GuardMarker
)

// ErrNotGuardable marks a condition that has no POSIX test form.
var ErrNotGuardable = errors.New("drone guard: condition has no shell test form")

// Guardable reports whether PrintGuard accepts e under sem.
func Guardable(e expr.Expr, sem expr.Semantics, opts ...Option) bool {
	o := optsOf(opts)
	_, err := printer{sem, o}.test(o.readRefName(e))
	return err == nil
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
	o := optsOf(opts)
	t, err := printer{sem, o}.test(o.readRefName(e))
	if err != nil {
		return "", err
	}
	return guardOpen + t + guardClose, nil
}

// ParseGuard inverts PrintGuard. It accepts only text that PrintGuard writes.
func ParseGuard(line string) (expr.Expr, bool) {
	body, ok := strings.CutPrefix(line, guardOpen)
	if !ok {
		return nil, false
	}
	body, ok = strings.CutSuffix(body, guardClose)
	if !ok {
		return nil, false
	}
	p := &guardParser{s: body}
	e, ok := p.primary()
	if !ok || p.i != len(p.s) {
		return nil, false
	}
	if back, err := PrintGuard(e, expr.Semantics{}); err != nil || back != line {
		return nil, false
	}
	return e, true
}

// AddGuard puts the guard for e before the script lines.
func AddGuard(script []string, e expr.Expr, sem expr.Semantics) ([]string, error) {
	line, err := PrintGuard(e, sem)
	if err != nil {
		return nil, err
	}
	return append([]string{line}, script...), nil
}

// RemoveGuard inverts AddGuard: it strips a leading guard line and returns its condition.
func RemoveGuard(script []string) ([]string, expr.Expr, bool) {
	if len(script) == 0 {
		return script, nil, false
	}
	e, ok := ParseGuard(script[0])
	if !ok {
		return script, nil, false
	}
	return script[1:], e, true
}

// printTest writes e as a shell test. It wraps every node that is not a single [ ] test in a group, so that
// `!` and the left-to-right `&&`/`||` of the shell cannot regroup the AST.
type printer struct {
	sem expr.Semantics
	opt options
}

func (p printer) test(e expr.Expr) (string, error) {
	t, err := p.node(e)
	if err != nil {
		return "", err
	}
	if b, ok := e.(expr.Binary); ok && (b.Op == expr.OpAnd || b.Op == expr.OpOr) || isNot(e) {
		return "( " + t + " )", nil
	}
	return t, nil
}

func isNot(e expr.Expr) bool {
	u, ok := e.(expr.Unary)
	return ok && u.Op == expr.OpNot
}

func (p printer) node(e expr.Expr) (string, error) {
	switch v := e.(type) {
	case expr.Unary:
		if v.Op != expr.OpNot {
			return "", fmt.Errorf("%w: operator %q", ErrNotGuardable, v.Op)
		}
		x, err := p.test(v.X)
		return "! " + x, err
	case expr.Binary:
		return p.binary(v)
	}
	return "", fmt.Errorf("%w: %T", ErrNotGuardable, e)
}

func (p printer) binary(b expr.Binary) (string, error) {
	switch b.Op {
	case expr.OpAnd, expr.OpOr:
		// A same-operator left child chains bare, as the shell reads a chain left to right.
		l, err := p.chainLeft(b)
		if err != nil {
			return "", err
		}
		r, err := p.test(b.R)
		if err != nil {
			return "", err
		}
		return l + " " + b.Op + " " + r, nil
	case expr.OpEq, expr.OpNe:
		if isEventRef(b.L) || isEventRef(b.R) {
			return p.eventCompare(b)
		}
		l, r, err := p.operands(b)
		if err != nil {
			return "", err
		}
		op := "="
		if b.Op == expr.OpNe {
			op = "!="
		}
		return "[ " + l + " " + op + " " + r + " ]", nil
	}
	return "", fmt.Errorf("%w: operator %q", ErrNotGuardable, b.Op)
}

func (p printer) chainLeft(b expr.Binary) (string, error) {
	if l, ok := b.L.(expr.Binary); ok && l.Op == b.Op {
		return p.node(l)
	}
	return p.test(b.L)
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
func (p printer) eventCompare(b expr.Binary) (string, error) {
	refLeft := isEventRef(b.L)
	other := b.R
	if !refLeft {
		other = b.L
	}
	lit, ok := other.(expr.Lit)
	if !ok || lit.Kind != expr.LitStr {
		return "", fmt.Errorf("%w: an event ref compares only with a string literal", ErrNotGuardable)
	}
	vals, ok := droneEvents(lit.Str, p.sem.CaseInsensitiveEq)
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
			tests[i] = "[ " + eventVar + " " + op + " " + quote(v) + " ]"
		} else {
			tests[i] = "[ " + quote(v) + " " + op + " " + eventVar + " ]"
		}
	}
	if len(tests) == 1 {
		return tests[0], nil
	}
	return "( " + strings.Join(tests, join) + " )", nil
}

// operands writes both sides of a comparison. A folding source needs one ref and one uncased literal, the
// only pair that the case-sensitive shell test reads as the source does.
func (p printer) operands(b expr.Binary) (string, string, error) {
	_, lIsRef := b.L.(expr.Ref)
	_, rIsRef := b.R.(expr.Ref)
	lLit, lIsLit := b.L.(expr.Lit)
	rLit, rIsLit := b.R.(expr.Lit)
	uncasedPair := lIsRef && rIsLit && p.opt.exact(rLit.Str, p.sem) || lIsLit && rIsRef && p.opt.exact(lLit.Str, p.sem)
	if p.sem.CaseInsensitiveEq && !uncasedPair {
		return "", "", fmt.Errorf("%w: a case-insensitive comparison needs a ref and an uncased literal", ErrNotGuardable)
	}
	l, err := operand(b.L)
	if err != nil {
		return "", "", err
	}
	r, err := operand(b.R)
	return l, r, err
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// operand writes a ref as a quoted variable expansion and a string as a single-quoted word.
func operand(e expr.Expr) (string, error) {
	switch v := e.(type) {
	case expr.Ref:
		name, ok := VarName(v)
		if !ok {
			return "", fmt.Errorf("%w: ref %s has no Drone variable", ErrNotGuardable, strings.Join(v.Path, "."))
		}
		return `"$` + name + `"`, nil
	case expr.Lit:
		if v.Kind != expr.LitStr || strings.ContainsAny(v.Str, "\n\r\x00") {
			return "", fmt.Errorf("%w: literal %+v", ErrNotGuardable, v)
		}
		return quote(v.Str), nil
	}
	return "", fmt.Errorf("%w: operand %T", ErrNotGuardable, e)
}

type guardParser struct {
	s string
	i int
}

func (p *guardParser) eat(tok string) bool {
	if strings.HasPrefix(p.s[p.i:], tok) {
		p.i += len(tok)
		return true
	}
	return false
}

// expr reads unit (op unit)* with one operator for the whole chain, folded to the left.
func (p *guardParser) expr() (expr.Expr, bool) {
	l, ok := p.unit()
	if !ok {
		return nil, false
	}
	chain := ""
	for {
		op := ""
		switch {
		case strings.HasPrefix(p.s[p.i:], " && "):
			op = expr.OpAnd
		case strings.HasPrefix(p.s[p.i:], " || "):
			op = expr.OpOr
		default:
			return l, true
		}
		if chain != "" && chain != op {
			return nil, false
		}
		chain = op
		p.i += len(op) + 2
		r, ok := p.unit()
		if !ok {
			return nil, false
		}
		l = expr.Binary{Op: op, L: l, R: r}
	}
}

func (p *guardParser) unit() (expr.Expr, bool) {
	if p.eat("! ") {
		x, ok := p.primary()
		return expr.Unary{Op: expr.OpNot, X: x}, ok
	}
	return p.primary()
}

func (p *guardParser) primary() (expr.Expr, bool) {
	if p.eat("( ") {
		e, ok := p.expr()
		return collapsePush(e), ok && p.eat(" )")
	}
	if !p.eat("[ ") {
		return nil, false
	}
	l, ok := p.operand()
	if !ok {
		return nil, false
	}
	op := ""
	switch {
	case p.eat(" = "):
		op = expr.OpEq
	case p.eat(" != "):
		op = expr.OpNe
	default:
		return nil, false
	}
	r, ok := p.operand()
	if !ok || !p.eat(" ]") {
		return nil, false
	}
	return canonEventCompare(expr.Binary{Op: op, L: l, R: r})
}

func (p *guardParser) operand() (expr.Expr, bool) {
	if p.eat(`"$`) {
		end := strings.IndexByte(p.s[p.i:], '"')
		if end < 0 {
			return nil, false
		}
		name := p.s[p.i : p.i+end]
		p.i += end + 1
		if !envName.MatchString(name) {
			return nil, false
		}
		return Ref(name), true
	}
	var sb strings.Builder
	pieces := 0
	for {
		switch {
		case p.eat(`\'`):
			sb.WriteByte('\'')
		case p.eat("'"):
			end := strings.IndexByte(p.s[p.i:], '\'')
			if end < 0 {
				return nil, false
			}
			sb.WriteString(p.s[p.i : p.i+end])
			p.i += end + 1
		default:
			return expr.Lit{Kind: expr.LitStr, Str: sb.String()}, pieces > 0
		}
		pieces++
	}
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
