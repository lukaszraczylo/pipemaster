package woodpecker

import (
	"errors"
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const maxDepth = expr.MaxDepth

// asciiSpace is what the lexer skips; unicode spaces are tokens, not padding.
const asciiSpace = " \t\n\r\f\v"

// Binding strength, loosest first, matching expr-lang (or 10, and 15, comparisons 20, not 50).
const (
	precOr = iota + 1
	precAnd
	precCmp
	precNot
	precAtom
)

// errUnsupported marks valid expr-lang the AST cannot model; Parse turns it into an Opaque.
var errUnsupported = errors.New("unsupported")

type parser struct {
	toks  []token
	i     int
	depth int
}

// Parse parses an evaluate expression. Valid expr-lang outside the subset (numbers, calls, members,
// arithmetic, ternaries, raw strings, a regex that is not a string literal) yields an Opaque holding the
// source text. `x in ["a", "b"]` becomes `x == "a" || x == "b"`, which expr-lang evaluates the same way.
func Parse(src string) (expr.Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	e, err := p.expression(precOr)
	if err == nil && p.peek().kind != tEOF {
		err = p.unexpected()
	}
	switch {
	case err == nil:
		return e, nil
	case !errors.Is(err, errUnsupported) || !balanced(toks):
		return nil, err
	}
	text := strings.Trim(src, asciiSpace)
	if _, _, ok := ParseGlob(expr.Opaque{Dialect: Dialect, Text: text}); ok {
		return nil, syntaxErr(0, "text is reserved for filter globs")
	}
	return expr.Opaque{Dialect: Dialect, Text: text}, nil
}

// balanced is true when parens and brackets nest, so an unsupported expression is well-formed enough to keep.
func balanced(toks []token) bool {
	var stack []tokKind
	for _, t := range toks {
		switch t.kind {
		case tLParen, tLBrack:
			stack = append(stack, t.kind)
		case tRParen, tRBrack:
			want := tLParen
			if t.kind == tRBrack {
				want = tLBrack
			}
			if len(stack) == 0 || stack[len(stack)-1] != want {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

// unexpected classifies the token the grammar cannot place: syntax expr-lang accepts (a call or index after
// an operand, `not in`, other operators) is unsupported, anything else is an error.
func (p *parser) unexpected() error {
	t := p.peek()
	switch t.kind {
	case tOther, tLParen, tLBrack, tNot:
		return errUnsupported
	}
	return syntaxErr(t.pos, "unexpected token")
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > maxDepth {
		return syntaxErr(p.peek().pos, "expression nested deeper than %d", maxDepth)
	}
	return nil
}

var cmpOps = map[tokKind]string{tEq: expr.OpEq, tNe: expr.OpNe}

func opPrec(k tokKind) int {
	switch k {
	case tOr:
		return precOr
	case tAnd:
		return precAnd
	case tEq, tNe, tMatches, tContains, tStartsWith, tEndsWith, tIn:
		return precCmp
	}
	return 0
}

func (p *parser) expression(minPrec int) (expr.Expr, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		k := p.peek().kind
		prec := opPrec(k)
		if prec == 0 || prec < minPrec {
			return left, nil
		}
		p.next()
		if left, err = p.binary(left, k, prec); err != nil {
			return nil, err
		}
	}
}

func (p *parser) binary(left expr.Expr, k tokKind, prec int) (expr.Expr, error) {
	switch k {
	case tMatches:
		return p.matches(left)
	case tIn:
		return p.in(left)
	}
	right, err := p.expression(prec + 1)
	if err != nil {
		return nil, err
	}
	switch k {
	case tOr:
		return expr.Binary{Op: expr.OpOr, L: left, R: right}, nil
	case tAnd:
		return expr.Binary{Op: expr.OpAnd, L: left, R: right}, nil
	case tContains:
		return expr.Call{Name: "contains", Args: []expr.Expr{left, right}}, nil
	case tStartsWith:
		return expr.Call{Name: "startsWith", Args: []expr.Expr{left, right}}, nil
	case tEndsWith:
		return expr.Call{Name: "endsWith", Args: []expr.Expr{left, right}}, nil
	}
	return expr.Binary{Op: cmpOps[k], L: left, R: right}, nil
}

func (p *parser) matches(left expr.Expr) (expr.Expr, error) {
	t := p.peek()
	if t.kind != tStr {
		return nil, errUnsupported
	}
	p.next()
	if _, err := regexp.Compile(t.text); err != nil {
		return nil, syntaxErr(t.pos, "invalid regular expression: %v", err)
	}
	return expr.Binary{Op: expr.OpMatch, L: left, R: expr.Lit{Kind: expr.LitStr, Str: t.text}}, nil
}

// in reads `[ "a", "b" ]` after a ref and builds the OR of equalities; the left chain keeps the depth flat.
func (p *parser) in(left expr.Expr) (expr.Expr, error) {
	r, ok := left.(expr.Ref)
	if !ok || p.next().kind != tLBrack {
		return nil, errUnsupported
	}
	var out expr.Expr
	for {
		t := p.next()
		if t.kind != tStr {
			return nil, errUnsupported
		}
		eq := expr.Binary{Op: expr.OpEq, L: r, R: expr.Lit{Kind: expr.LitStr, Str: t.text}}
		if out == nil {
			out = eq
		} else {
			out = expr.Binary{Op: expr.OpOr, L: out, R: eq}
		}
		switch p.next().kind {
		case tComma:
		case tRBrack:
			return out, nil
		default:
			return nil, errUnsupported
		}
	}
}

func (p *parser) unary() (expr.Expr, error) {
	if p.peek().kind != tNot {
		return p.primary()
	}
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	p.next()
	x, err := p.unary()
	if err != nil {
		return nil, err
	}
	return expr.Unary{Op: expr.OpNot, X: x}, nil
}

func (p *parser) primary() (expr.Expr, error) {
	t := p.peek()
	switch t.kind {
	case tIdent:
		p.next()
		return ref(t.text), nil
	case tStr:
		p.next()
		return expr.Lit{Kind: expr.LitStr, Str: t.text}, nil
	case tTrue, tFalse:
		p.next()
		return expr.Lit{Kind: expr.LitBool, Bool: t.kind == tTrue}, nil
	case tNil:
		p.next()
		return expr.Lit{}, nil
	case tLParen:
		p.next()
		if err := p.enter(); err != nil {
			return nil, err
		}
		defer func() { p.depth-- }()
		e, err := p.expression(precOr)
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tRParen {
			return nil, p.unexpected()
		}
		p.next()
		return e, nil
	case tOther, tLBrack:
		return nil, errUnsupported
	}
	return nil, syntaxErr(t.pos, "unexpected token")
}
