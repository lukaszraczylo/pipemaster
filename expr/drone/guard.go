package drone

import (
	"errors"
	"fmt"
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

// Guardable reports whether PrintGuard accepts e.
func Guardable(e expr.Expr) bool {
	_, err := printTest(e)
	return err == nil
}

// PrintGuard writes the script line that skips the rest of a step when e is false. The step then shows
// success instead of skipped, so callers must report the guard as an emulation.
func PrintGuard(e expr.Expr) (string, error) {
	t, err := printTest(e)
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
	if back, err := PrintGuard(e); err != nil || back != line {
		return nil, false
	}
	return e, true
}

// AddGuard puts the guard for e before the script lines.
func AddGuard(script []string, e expr.Expr) ([]string, error) {
	line, err := PrintGuard(e)
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
func printTest(e expr.Expr) (string, error) {
	t, err := printNode(e)
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

func printNode(e expr.Expr) (string, error) {
	switch v := e.(type) {
	case expr.Unary:
		if v.Op != expr.OpNot {
			return "", fmt.Errorf("%w: operator %q", ErrNotGuardable, v.Op)
		}
		x, err := printTest(v.X)
		return "! " + x, err
	case expr.Binary:
		return printBinary(v)
	}
	return "", fmt.Errorf("%w: %T", ErrNotGuardable, e)
}

func printBinary(b expr.Binary) (string, error) {
	switch b.Op {
	case expr.OpAnd, expr.OpOr:
		// A same-operator left child chains bare, as the shell reads a chain left to right.
		l, err := printChainLeft(b)
		if err != nil {
			return "", err
		}
		r, err := printTest(b.R)
		if err != nil {
			return "", err
		}
		return l + " " + b.Op + " " + r, nil
	case expr.OpEq, expr.OpNe:
		l, err := operand(b.L)
		if err != nil {
			return "", err
		}
		r, err := operand(b.R)
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

func printChainLeft(b expr.Binary) (string, error) {
	if l, ok := b.L.(expr.Binary); ok && l.Op == b.Op {
		return printNode(l)
	}
	return printTest(b.L)
}

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
		return "'" + strings.ReplaceAll(v.Str, "'", `'\''`) + "'", nil
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
		return e, ok && p.eat(" )")
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
	return expr.Binary{Op: op, L: l, R: r}, true
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
