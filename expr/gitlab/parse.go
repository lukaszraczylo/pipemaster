package gitlab

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// maxDepth bounds nesting so hostile input cannot exhaust the stack.
const maxDepth = expr.MaxDepth

// Binding strength, loosest first, matching the GitLab lexeme precedence (|| 12, && 11, comparison 10, ! 1).
const (
	precOr = iota + 1
	precAnd
	precCmp
	precNot
	precAtom
)

type parser struct {
	toks   []token
	i      int
	depth  int
	opaque bool
}

// Parse parses a rules:if expression. Valid syntax the AST cannot model (a regex flag other than i, a
// right-hand side of =~ or !~ that is not a literal pattern, $[[ inputs.x ]]) yields an Opaque holding
// the source text. Strings have no escapes; the quote form chosen only decides which quote may appear inside.
func Parse(src string) (expr.Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	e, err := p.expression(precOr)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, syntaxErr(t.pos, "unexpected token")
	}
	if p.opaque {
		return expr.Opaque{Dialect: Dialect, Text: strings.TrimSpace(src)}, nil
	}
	return e, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

var binOps = map[tokKind]struct {
	op   string
	prec int
}{
	tOr: {expr.OpOr, precOr}, tAnd: {expr.OpAnd, precAnd},
	tEq: {expr.OpEq, precCmp}, tNe: {expr.OpNe, precCmp},
	tMatch: {expr.OpMatch, precCmp}, tNoMatch: {expr.OpNoMatch, precCmp},
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > maxDepth {
		return syntaxErr(p.peek().pos, "expression nested deeper than %d", maxDepth)
	}
	return nil
}

func (p *parser) expression(minPrec int) (expr.Expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		op, ok := binOps[p.peek().kind]
		if !ok || op.prec < minPrec {
			return left, nil
		}
		p.next()
		var right expr.Expr
		if op.op == expr.OpMatch || op.op == expr.OpNoMatch {
			right, err = p.regexOperand()
		} else {
			right, err = p.expression(op.prec + 1)
		}
		if err != nil {
			return nil, err
		}
		left = expr.Binary{Op: op.op, L: left, R: right}
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
	t := p.next()
	switch t.kind {
	case tVar:
		return ref(t.text), nil
	case tStr:
		return expr.Lit{Kind: expr.LitStr, Str: t.text}, nil
	case tNull:
		return expr.Lit{}, nil
	case tBool:
		return expr.Lit{Kind: expr.LitBool, Bool: t.text == "true"}, nil
	case tInput:
		p.opaque = true
		return expr.Lit{}, nil
	case tLParen:
		e, err := p.expression(precOr)
		if err != nil {
			return nil, err
		}
		if c := p.next(); c.kind != tRParen {
			return nil, syntaxErr(c.pos, "expected ')'")
		}
		return e, nil
	case tPattern:
		return nil, syntaxErr(t.pos, "regular expression outside =~ or !~")
	}
	return nil, syntaxErr(t.pos, "unexpected token")
}

// regexOperand reads the right side of =~ / !~. Only a literal /pattern/ (flags i or none) is modelled:
// GitLab evaluates a variable or string there as a pattern at run time, which has no static form.
func (p *parser) regexOperand() (expr.Expr, error) {
	t := p.peek()
	if t.kind != tPattern {
		if _, err := p.unary(); err != nil {
			return nil, err
		}
		p.opaque = true
		return expr.Lit{}, nil
	}
	p.next()
	if _, err := regexp.Compile(t.text); err != nil {
		return nil, syntaxErr(t.pos, "invalid regular expression: %v", err)
	}
	switch t.flags {
	case "":
		return expr.Lit{Kind: expr.LitStr, Str: t.text}, nil
	case "i":
		return expr.Lit{Kind: expr.LitStr, Str: "(?i)" + t.text}, nil
	}
	p.opaque = true
	return expr.Lit{}, nil
}
