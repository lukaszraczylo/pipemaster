package argo

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const (
	exprOpen = "{{="
	litTrue  = "true"
	litFalse = "false"
	notOp    = "!"
)

// disabled is the `when` that skips its task: the printed form of the literal false.
const disabled = "{{= false }}"

// DisabledWhen is the fail-safe for a condition that has no `when` form: it skips the task.
func DisabledWhen() string { return disabled }

// IsDisabled reports whether when is the literal false in either spelling.
func IsDisabled(when string) bool {
	l, ok := FromWhen(when, ScopeTask).(expr.Lit)
	return ok && l.Kind == expr.LitBool && !l.Bool
}

// OpaqueWhen reads the `when` text back from an Opaque node that FromWhen made.
func OpaqueWhen(o expr.Opaque) (string, bool) {
	if o.Dialect != Dialect {
		return "", false
	}
	return o.Text, true
}

// FromWhen reads a task's `when` text as an expression. It accepts the expr-lang form `{{= ... }}` and the
// govaluate form over {{simple}} tags, where a bare word on the right of a comparison is a string. Both read
// ==, !=, &&, ||, ! and parentheses over refs, strings, numbers and booleans. Any other text, such as a regex
// match or a function call, is one Opaque node that holds the text. Empty text reads as nil.
func FromWhen(when string, s Scope) expr.Expr {
	t := strings.TrimSpace(when)
	if t == "" {
		return nil
	}
	if e, ok := parse(t, s); ok {
		return e
	}
	return expr.Opaque{Dialect: Dialect, Text: when}
}

func parse(t string, s Scope) (expr.Expr, bool) {
	gov := true
	if rest, ok := strings.CutPrefix(t, exprOpen); ok {
		// One tag only: Argo ends the tag at the first }}.
		if end := strings.Index(rest, tagClose); end >= 0 && end == len(rest)-len(tagClose) {
			t, gov = rest[:end], false
		}
	}
	toks, ok := lex(t, s, gov)
	if !ok {
		return nil, false
	}
	p := &parser{toks: toks}
	e, ok := p.or()
	if !ok || p.i != len(toks) {
		return nil, false
	}
	return e, true
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() kind {
	if p.i >= len(p.toks) {
		return 0
	}
	return p.toks[p.i].kind
}

func (p *parser) or() (expr.Expr, bool) { return p.chain(kOr, expr.OpOr, p.and) }

func (p *parser) and() (expr.Expr, bool) { return p.chain(kAnd, expr.OpAnd, p.unary) }

func (p *parser) chain(k kind, op string, operand func() (expr.Expr, bool)) (expr.Expr, bool) {
	l, ok := operand()
	for ok && p.peek() == k {
		p.i++
		var r expr.Expr
		if r, ok = operand(); ok {
			l = expr.Binary{Op: op, L: l, R: r}
		}
	}
	return l, ok
}

// unary reads a negation. `!` binds tighter than `==` in expr-lang, so its operand is a group, a boolean or
// another negation; `!x == y` is outside the subset.
func (p *parser) unary() (expr.Expr, bool) {
	if p.peek() != kNot {
		return p.primary()
	}
	p.i++
	switch p.peek() {
	case kLParen, kBool, kNot:
		x, ok := p.unary()
		return expr.Unary{Op: expr.OpNot, X: x}, ok
	}
	return nil, false
}

func (p *parser) primary() (expr.Expr, bool) {
	if p.peek() == kLParen {
		p.i++
		e, ok := p.or()
		if !ok || p.peek() != kRParen {
			return nil, false
		}
		p.i++
		return e, true
	}
	l, ok := p.value(false)
	if !ok {
		return nil, false
	}
	var op string
	switch p.peek() {
	case kEq:
		op = expr.OpEq
	case kNe:
		op = expr.OpNe
	default:
		// A boolean needs no comparison; any other value is not a condition.
		if lit, isLit := l.(expr.Lit); isLit && lit.Kind == expr.LitBool {
			return l, true
		}
		return nil, false
	}
	p.i++
	r, ok := p.value(true)
	if !ok {
		return nil, false
	}
	return expr.Binary{Op: op, L: l, R: r}, true
}

// value reads one operand. A bare govaluate word stands only on the right of a comparison.
func (p *parser) value(right bool) (expr.Expr, bool) {
	if p.i >= len(p.toks) {
		return nil, false
	}
	t := p.toks[p.i]
	p.i++
	switch t.kind {
	case kRef:
		return t.ref, true
	case kStr:
		return expr.Lit{Kind: expr.LitStr, Str: t.str}, right || !t.bare
	case kNum:
		return expr.Lit{Kind: expr.LitNum, Num: t.num}, true
	case kBool:
		return expr.Lit{Kind: expr.LitBool, Bool: t.bool}, true
	}
	return nil, false
}

// ToWhen writes e as `{{= ... }}`, the one form that quotes safely: govaluate breaks on a value that holds a
// quote. It prints the ==, != , &&, ||, ! subset over refs and string, number and boolean literals, and an
// Opaque{argo} node that is the whole condition as its own text. Anything else fails.
func ToWhen(e expr.Expr, s Scope) (string, bool) {
	if o, ok := e.(expr.Opaque); ok {
		return OpaqueWhen(o)
	}
	body, ok := printCond(e, s)
	if !ok {
		return "", false
	}
	return exprOpen + " " + body + " " + tagClose, true
}

func printCond(e expr.Expr, s Scope) (string, bool) {
	switch v := e.(type) {
	case expr.Lit:
		if v.Kind != expr.LitBool {
			return "", false
		}
		return litBool(v.Bool), true
	case expr.Unary:
		if v.Op != expr.OpNot {
			return "", false
		}
		x, ok := printCond(v.X, s)
		if _, grouped := v.X.(expr.Binary); grouped {
			x = "(" + x + ")"
		}
		return notOp + x, ok
	case expr.Binary:
		return printBinary(v, s)
	}
	return "", false
}

func printBinary(b expr.Binary, s Scope) (string, bool) {
	switch b.Op {
	case expr.OpEq, expr.OpNe:
		l, lok := printValue(b.L, s)
		r, rok := printValue(b.R, s)
		return l + " " + b.Op + " " + r, lok && rok
	case expr.OpAnd, expr.OpOr:
		l, lok := printCond(b.L, s)
		r, rok := printCond(b.R, s)
		// A chain of one operator reads left to right, so only the right operand needs its group to keep its shape.
		if isLogic(b.L) && b.L.(expr.Binary).Op != b.Op {
			l = "(" + l + ")"
		}
		if isLogic(b.R) {
			r = "(" + r + ")"
		}
		return l + " " + b.Op + " " + r, lok && rok
	}
	return "", false
}

func isLogic(e expr.Expr) bool {
	b, ok := e.(expr.Binary)
	return ok && (b.Op == expr.OpAnd || b.Op == expr.OpOr)
}

func printValue(e expr.Expr, s Scope) (string, bool) {
	switch v := e.(type) {
	case expr.Ref:
		return RefExpr(v, s)
	case expr.Lit:
		switch v.Kind {
		case expr.LitStr:
			return quote(v.Str)
		case expr.LitNum:
			if math.IsNaN(v.Num) || math.IsInf(v.Num, 0) || math.Signbit(v.Num) {
				return "", false
			}
			return strconv.FormatFloat(v.Num, 'f', -1, 64), true
		case expr.LitBool:
			return litBool(v.Bool), true
		}
	}
	return "", false
}

func litBool(b bool) string {
	if b {
		return litTrue
	}
	return litFalse
}

// quote writes s as a single-quoted expr-lang string. The lexer of expr-lang at v1.17.5 reads \' and \\ in
// a single-quoted string (parser/lexer/lexer.go scanEscape, parser/lexer/utils.go unescapeChar) and ends a
// string at a raw newline, so control characters print as escapes. A tag mark is refused: Argo ends the tag
// at the first }} and substitutes any {{ before the expression runs.
func quote(s string) (string, bool) {
	if !utf8.ValidString(s) || strings.Contains(s, tagOpen) || strings.Contains(s, tagClose) {
		return "", false
	}
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch {
		case r == '\'' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\x` + string("0123456789abcdef"[r>>4]) + string("0123456789abcdef"[r&0xf]))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String(), true
}
