// Package shguard prints and parses the POSIX shell guard line that a provider puts first in a script step
// when its condition has no native form. A Dialect supplies how a ref prints as a shell word.
package shguard

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Marker ends a guard line so Parse can tell it from a script line that the author wrote.
const Marker = "# pipemaster:guard"

const (
	guardOpen  = "if ! "
	guardClose = "; then exit 0; fi " + Marker
)

// ErrNotGuardable marks a condition that has no POSIX test form.
var ErrNotGuardable = errors.New("shell guard: condition has no shell test form")

// EnvName matches a shell variable name.
var EnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Dialect holds the parts of a guard that differ per provider. Only Sem, Name and Ref are required.
type Dialect struct {
	// Name gives the shell variable that holds a ref.
	Name func(expr.Ref) (string, bool)
	// Ref inverts Name.
	Ref func(name string) (expr.Ref, bool)
	// Prepare rewrites the condition before printing.
	Prepare func(expr.Expr) expr.Expr
	// Compare prints a == or != node itself. handled false leaves the node to the shared printer.
	Compare func(expr.Binary) (text string, handled bool, err error)
	// Literal reports whether a string literal can appear in the script text. Nil means always.
	Literal func(lit string) bool
	// Exact reports whether a literal reads the same under Sem and under the shell test. Nil means always.
	Exact func(lit string) bool
	// Canon rewrites a parsed comparison, for example to a canonical literal. Nil keeps it.
	Canon func(expr.Binary) (expr.Expr, bool)
	// Group rewrites a parsed parenthesised group. Nil keeps it.
	Group func(expr.Expr) expr.Expr
	// Atom prints a call node as one test; handled false leaves the node unguardable. Nil means no call prints.
	Atom func(expr.Call) (text string, handled bool)
	// AtomRead reads the test that Atom printed at the head of s and returns the node and the bytes it used.
	AtomRead func(s string) (e expr.Expr, n int, ok bool)
	// Sem is the equality semantics of the source that wrote the condition.
	Sem expr.Semantics
}

// Quote writes s as a single-quoted shell word.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Guardable reports whether Print accepts e.
func (d Dialect) Guardable(e expr.Expr) bool {
	_, err := d.Print(e)
	return err == nil
}

// Print writes the script line that skips the rest of a step when e is false.
func (d Dialect) Print(e expr.Expr) (string, error) {
	if d.Prepare != nil {
		e = d.Prepare(e)
	}
	t, err := d.test(e)
	if err != nil {
		return "", err
	}
	return guardOpen + t + guardClose, nil
}

// Parse inverts Print. It accepts only text that Print writes under d.
func (d Dialect) Parse(line string) (expr.Expr, bool) {
	body, ok := strings.CutPrefix(line, guardOpen)
	if !ok {
		return nil, false
	}
	body, ok = strings.CutSuffix(body, guardClose)
	if !ok {
		return nil, false
	}
	p := &parser{d: d, s: body}
	e, ok := p.primary()
	if !ok || p.i != len(p.s) {
		return nil, false
	}
	if back, err := d.Print(e); err != nil || back != line {
		return nil, false
	}
	return e, true
}

// Add puts the guard for e before the script lines.
func (d Dialect) Add(script []string, e expr.Expr) ([]string, error) {
	line, err := d.Print(e)
	if err != nil {
		return nil, err
	}
	return append([]string{line}, script...), nil
}

// Remove inverts Add: it strips a leading guard line and returns its condition.
func (d Dialect) Remove(script []string) ([]string, expr.Expr, bool) {
	if len(script) == 0 {
		return script, nil, false
	}
	e, ok := d.Parse(script[0])
	if !ok {
		return script, nil, false
	}
	return script[1:], e, true
}

// test wraps every node that is not a single [ ] test in a group, so that `!` and the left-to-right `&&`/`||`
// of the shell cannot regroup the AST.
func (d Dialect) test(e expr.Expr) (string, error) {
	t, err := d.node(e)
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

func (d Dialect) node(e expr.Expr) (string, error) {
	switch v := e.(type) {
	case expr.Unary:
		if v.Op != expr.OpNot {
			return "", fmt.Errorf("%w: operator %q", ErrNotGuardable, v.Op)
		}
		x, err := d.test(v.X)
		return "! " + x, err
	case expr.Binary:
		return d.binary(v)
	case expr.Call:
		if d.Atom != nil {
			if t, ok := d.Atom(v); ok {
				return t, nil
			}
		}
	}
	return "", fmt.Errorf("%w: %T", ErrNotGuardable, e)
}

func (d Dialect) binary(b expr.Binary) (string, error) {
	switch b.Op {
	case expr.OpAnd, expr.OpOr:
		// A same-operator left child chains bare, as the shell reads a chain left to right.
		l, err := d.chainLeft(b)
		if err != nil {
			return "", err
		}
		r, err := d.test(b.R)
		if err != nil {
			return "", err
		}
		return l + " " + b.Op + " " + r, nil
	case expr.OpEq, expr.OpNe:
		if d.Compare != nil {
			if t, handled, err := d.Compare(b); handled {
				return t, err
			}
		}
		l, r, err := d.operands(b)
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

func (d Dialect) chainLeft(b expr.Binary) (string, error) {
	if l, ok := b.L.(expr.Binary); ok && l.Op == b.Op {
		return d.node(l)
	}
	return d.test(b.L)
}

func (d Dialect) exact(s string) bool { return d.Exact == nil || d.Exact(s) }

// operands writes both sides of a comparison. A folding source needs one ref and one uncased literal, the
// only pair that the case-sensitive shell test reads as the source does.
func (d Dialect) operands(b expr.Binary) (string, string, error) {
	_, lIsRef := b.L.(expr.Ref)
	_, rIsRef := b.R.(expr.Ref)
	lLit, lIsLit := b.L.(expr.Lit)
	rLit, rIsLit := b.R.(expr.Lit)
	uncasedPair := lIsRef && rIsLit && d.exact(rLit.Str) || lIsLit && rIsRef && d.exact(lLit.Str)
	if d.Sem.CaseInsensitiveEq && !uncasedPair {
		return "", "", fmt.Errorf("%w: a case-insensitive comparison needs a ref and an uncased literal", ErrNotGuardable)
	}
	l, err := d.operand(b.L)
	if err != nil {
		return "", "", err
	}
	r, err := d.operand(b.R)
	return l, r, err
}

// operand writes a ref as a quoted variable expansion and a string as a single-quoted word.
func (d Dialect) operand(e expr.Expr) (string, error) {
	switch v := e.(type) {
	case expr.Ref:
		name, ok := d.Name(v)
		if !ok || !EnvName.MatchString(name) {
			return "", fmt.Errorf("%w: ref %s has no shell variable", ErrNotGuardable, strings.Join(v.Path, "."))
		}
		return `"$` + name + `"`, nil
	case expr.Lit:
		if v.Kind != expr.LitStr || strings.ContainsAny(v.Str, "\n\r\x00") || d.Literal != nil && !d.Literal(v.Str) {
			return "", fmt.Errorf("%w: literal %+v", ErrNotGuardable, v)
		}
		return Quote(v.Str), nil
	}
	return "", fmt.Errorf("%w: operand %T", ErrNotGuardable, e)
}

type parser struct {
	d Dialect
	s string
	i int
}

func (p *parser) eat(tok string) bool {
	if strings.HasPrefix(p.s[p.i:], tok) {
		p.i += len(tok)
		return true
	}
	return false
}

// expr reads unit (op unit)* with one operator for the whole chain, folded to the left.
func (p *parser) expr() (expr.Expr, bool) {
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

func (p *parser) unit() (expr.Expr, bool) {
	if p.eat("! ") {
		x, ok := p.primary()
		return expr.Unary{Op: expr.OpNot, X: x}, ok
	}
	return p.primary()
}

func (p *parser) primary() (expr.Expr, bool) {
	if p.eat("( ") {
		e, ok := p.expr()
		if p.d.Group != nil {
			e = p.d.Group(e)
		}
		return e, ok && p.eat(" )")
	}
	if p.d.AtomRead != nil {
		if e, n, ok := p.d.AtomRead(p.s[p.i:]); ok {
			p.i += n
			return e, true
		}
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
	b := expr.Binary{Op: op, L: l, R: r}
	if p.d.Canon != nil {
		return p.d.Canon(b)
	}
	return b, true
}

func (p *parser) operand() (expr.Expr, bool) {
	if p.eat(`"$`) {
		end := strings.IndexByte(p.s[p.i:], '"')
		if end < 0 {
			return nil, false
		}
		name := p.s[p.i : p.i+end]
		p.i += end + 1
		if !EnvName.MatchString(name) {
			return nil, false
		}
		r, ok := p.d.Ref(name)
		return r, ok
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
