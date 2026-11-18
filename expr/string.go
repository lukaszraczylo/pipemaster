package expr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	opaquePrefix = "opaque("
	nestedOpaque = "@opaque("
)

var (
	topOpaqueRE  = regexp.MustCompile(`(?s)^opaque\(([^)]*)\): (.*)$`)
	keywordIdent = map[string]bool{"true": true, "false": true, "null": true}
)

// String renders e in the canonical form ParseCanonical reads back exactly.
// Binary operands that are themselves binary are always parenthesised, so precedence never matters when printing.
// A top-level Opaque keeps the `opaque(<dialect>): <text>` form; one nested in an operator, or whose dialect contains ')',
// prints as `@opaque('<dialect>', '<text>')`. Lit numbers must be finite, as non-finite values have no canonical text.
func String(e Expr) string {
	if o, ok := e.(Opaque); ok && !strings.Contains(o.Dialect, ")") {
		return opaquePrefix + o.Dialect + "): " + o.Text
	}
	var sb strings.Builder
	write(&sb, e)
	return sb.String()
}

func write(sb *strings.Builder, e Expr) {
	switch v := e.(type) {
	case Lit:
		switch v.Kind {
		case LitNull:
			sb.WriteString("null")
		case LitBool:
			sb.WriteString(strconv.FormatBool(v.Bool))
		case LitNum:
			sb.WriteString(strconv.FormatFloat(v.Num, 'g', -1, 64))
		case LitStr:
			sb.WriteString(QuoteSingle(v.Str))
		}
	case Ref:
		for i, seg := range v.Path {
			plain := IsIdent(seg) && (i != 0 || !keywordIdent[seg])
			switch {
			case plain && i == 0:
				sb.WriteString(seg)
			case plain:
				sb.WriteString("." + seg)
			default:
				sb.WriteString("[" + QuoteSingle(seg) + "]")
			}
		}
	case Unary:
		sb.WriteString(v.Op)
		operand(sb, v.X)
	case Binary:
		operand(sb, v.L)
		sb.WriteString(" " + v.Op + " ")
		operand(sb, v.R)
	case Call:
		sb.WriteString(v.Name + "(")
		for i, a := range v.Args {
			if i > 0 {
				sb.WriteString(", ")
			}
			write(sb, a)
		}
		sb.WriteString(")")
	case Opaque:
		sb.WriteString(nestedOpaque + QuoteSingle(v.Dialect) + ", " + QuoteSingle(v.Text) + ")")
	}
}

func operand(sb *strings.Builder, e Expr) {
	if _, ok := e.(Binary); ok {
		sb.WriteString("(")
		write(sb, e)
		sb.WriteString(")")
		return
	}
	write(sb, e)
}

// ParseCanonical parses the form String produces. Precedence, loosest first, is `||`, `&&`,
// the comparison and match operators (left-associative), then unary `!`.
func ParseCanonical(s string) (Expr, error) {
	if m := topOpaqueRE.FindStringSubmatch(s); m != nil {
		return Opaque{Dialect: m[1], Text: m[2]}, nil
	}
	p := &parser{s: s}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.i < len(p.s) {
		return nil, p.errf("unexpected %q", p.s[p.i:])
	}
	return e, nil
}

type parser struct {
	s     string
	i     int
	depth int
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > MaxDepth {
		return p.errf("expression nested deeper than %d", MaxDepth)
	}
	return nil
}

func (p *parser) leave() { p.depth-- }

func (p *parser) errf(format string, a ...any) error {
	return fmt.Errorf("parse expression %q at %d: %s", p.s, p.i, fmt.Sprintf(format, a...))
}

func (p *parser) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *parser) eat(tok string) bool {
	p.skip()
	if strings.HasPrefix(p.s[p.i:], tok) {
		p.i += len(tok)
		return true
	}
	return false
}

func (p *parser) or() (Expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.eat(OpOr) {
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = Binary{Op: OpOr, L: l, R: r}
	}
	return l, nil
}

func (p *parser) and() (Expr, error) {
	l, err := p.cmp()
	if err != nil {
		return nil, err
	}
	for p.eat(OpAnd) {
		r, err := p.cmp()
		if err != nil {
			return nil, err
		}
		l = Binary{Op: OpAnd, L: l, R: r}
	}
	return l, nil
}

// cmpOps lists two-character operators before their one-character prefixes.
var cmpOps = []string{OpEq, OpNe, OpLe, OpGe, OpMatch, OpNoMatch, OpLt, OpGt}

func (p *parser) cmp() (Expr, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		for _, c := range cmpOps {
			if p.eat(c) {
				op = c
				break
			}
		}
		if op == "" {
			return l, nil
		}
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = Binary{Op: op, L: l, R: r}
	}
}

func (p *parser) unary() (Expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer p.leave()
	p.skip()
	if p.i < len(p.s) && p.s[p.i] == '!' && !strings.HasPrefix(p.s[p.i:], "!=") && !strings.HasPrefix(p.s[p.i:], "!~") {
		p.i++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return Unary{Op: OpNot, X: x}, nil
	}
	return p.primary()
}

func (p *parser) primary() (Expr, error) {
	p.skip()
	if p.i >= len(p.s) {
		return nil, p.errf("unexpected end")
	}
	c := p.s[p.i]
	switch {
	case c == '(':
		p.i++
		e, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.eat(")") {
			return nil, p.errf("missing )")
		}
		return e, nil
	case c == '\'':
		s, err := p.str()
		return Lit{Kind: LitStr, Str: s}, err
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case strings.HasPrefix(p.s[p.i:], nestedOpaque):
		return p.opaque()
	case c == '[' || IsIdentStart(c):
		return p.refOrCall()
	}
	return nil, p.errf("unexpected %q", string(c))
}

func (p *parser) ident() (string, bool) {
	start := p.i
	if p.i >= len(p.s) || !IsIdentStart(p.s[p.i]) {
		return "", false
	}
	for p.i < len(p.s) && IsIdentPart(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i], true
}

func (p *parser) str() (string, error) {
	p.i++ // opening quote
	var sb strings.Builder
	for p.i < len(p.s) {
		if p.s[p.i] == '\'' {
			if p.i+1 < len(p.s) && p.s[p.i+1] == '\'' {
				sb.WriteByte('\'')
				p.i += 2
				continue
			}
			p.i++
			return sb.String(), nil
		}
		sb.WriteByte(p.s[p.i])
		p.i++
	}
	return "", p.errf("unterminated string")
}

func (p *parser) number() (Expr, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	if digits() == 0 {
		return nil, p.errf("bad number")
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if digits() == 0 {
			return nil, p.errf("bad number")
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if digits() == 0 {
			return nil, p.errf("bad number")
		}
	}
	f, err := strconv.ParseFloat(p.s[start:p.i], 64)
	if err != nil {
		return nil, p.errf("bad number: %v", err)
	}
	return Lit{Kind: LitNum, Num: f}, nil
}

func (p *parser) opaque() (Expr, error) {
	p.i += len(nestedOpaque)
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != '\'' {
		return nil, p.errf("@opaque needs a dialect string")
	}
	d, err := p.str()
	if err != nil {
		return nil, err
	}
	if !p.eat(",") {
		return nil, p.errf("@opaque needs a text argument")
	}
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != '\'' {
		return nil, p.errf("@opaque needs a text string")
	}
	t, err := p.str()
	if err != nil {
		return nil, err
	}
	if !p.eat(")") {
		return nil, p.errf("missing )")
	}
	return Opaque{Dialect: d, Text: t}, nil
}

func (p *parser) refOrCall() (Expr, error) {
	var path []string
	if p.s[p.i] == '[' {
		seg, err := p.bracket()
		if err != nil {
			return nil, err
		}
		path = append(path, seg)
	} else {
		id, _ := p.ident()
		if p.i < len(p.s) && p.s[p.i] == '(' {
			return p.call(id)
		}
		if keywordIdent[id] && !p.more() {
			switch id {
			case "true":
				return Lit{Kind: LitBool, Bool: true}, nil
			case "false":
				return Lit{Kind: LitBool}, nil
			}
			return Lit{}, nil
		}
		path = append(path, id)
	}
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '.':
			p.i++
			id, ok := p.ident()
			if !ok {
				return nil, p.errf("expected name after '.'")
			}
			path = append(path, id)
		case '[':
			seg, err := p.bracket()
			if err != nil {
				return nil, err
			}
			path = append(path, seg)
		default:
			return Ref{Path: path}, nil
		}
	}
	return Ref{Path: path}, nil
}

func (p *parser) more() bool {
	return p.i < len(p.s) && (p.s[p.i] == '.' || p.s[p.i] == '[')
}

func (p *parser) bracket() (string, error) {
	p.i++ // '['
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != '\'' {
		return "", p.errf("expected quoted name in []")
	}
	seg, err := p.str()
	if err != nil {
		return "", err
	}
	if !p.eat("]") {
		return "", p.errf("missing ]")
	}
	return seg, nil
}

func (p *parser) call(name string) (Expr, error) {
	p.i++ // '('
	var args []Expr
	if p.eat(")") {
		return Call{Name: name}, nil
	}
	for {
		a, err := p.or()
		if err != nil {
			return nil, err
		}
		args = append(args, a)
		if p.eat(")") {
			return Call{Name: name, Args: args}, nil
		}
		if !p.eat(",") {
			return nil, p.errf("expected , or ) in call")
		}
	}
}
