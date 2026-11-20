package gha

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Dialect is the Opaque dialect name and the root of provider-native refs.
const Dialect = "gha"

// githubContext is the context name that every dialect alias maps to.
const githubContext = "github"

// maxDepth mirrors the GitHub expression parser's nesting limit.
const maxDepth = expr.MaxDepth

// functions maps the lower-cased function name to its canonical spelling.
var functions = map[string]string{}

func init() {
	for _, n := range []string{
		"contains", "startsWith", "endsWith", "format", "join", "toJSON", "fromJSON", "hashFiles",
		"success", "failure", "always", "cancelled",
	} {
		functions[strings.ToLower(n)] = n
	}
}

type parser struct {
	aliases []string
	toks    []token
	i       int
	depth   int
	opaque  bool
}

// Parse parses a bare GitHub Actions expression (no ${{ }}). Valid syntax the AST cannot model
// (dynamic index, object filter, access on a call result or literal, unknown function, NaN/Infinity)
// yields an Opaque holding the source text.
func Parse(src string) (expr.Expr, error) { return ParseWith(src, nil) }

// ParseWith is Parse that also reads each aliases context name as the github context.
func ParseWith(src string, aliases []string) (expr.Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, aliases: aliases}
	e, err := p.expression(1)
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, syntaxErr(t.pos, "unexpected token")
	}
	if p.opaque {
		return expr.Opaque{Dialect: Dialect, Text: strings.TrimSpace(src)}, nil
	}
	return foldRefs(e), nil
}

// foldRefs reads the text GHA needs for the canonical branch and tag refs, (ref_type == 'branch' && ref_name) || ”,
// back as the ref, so the printer and the parser agree.
func foldRefs(x expr.Expr) expr.Expr {
	switch v := x.(type) {
	case expr.Binary:
		if kind, ok := refShape(v); ok {
			return expr.Ref{Path: []string{kind}}
		}
		v.L, v.R = foldRefs(v.L), foldRefs(v.R)
		return v
	case expr.Unary:
		v.X = foldRefs(v.X)
		return v
	case expr.Call:
		if len(v.Args) == 0 {
			return v
		}
		args := make([]expr.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = foldRefs(a)
		}
		v.Args = args
		return v
	}
	return x
}

func refShape(b expr.Binary) (string, bool) {
	empty, ok := b.R.(expr.Lit)
	if b.Op != expr.OpOr || !ok || empty.Kind != expr.LitStr || empty.Str != "" {
		return "", false
	}
	and, ok := b.L.(expr.Binary)
	if !ok || and.Op != expr.OpAnd || !isRef(and.R, expr.CtxRefName) {
		return "", false
	}
	eq, ok := and.L.(expr.Binary)
	if !ok || eq.Op != expr.OpEq || !isRef(eq.L, expr.CtxRefType) {
		return "", false
	}
	kind, ok := eq.R.(expr.Lit)
	if !ok || kind.Kind != expr.LitStr {
		return "", false
	}
	switch kind.Str {
	case "branch":
		return expr.CtxBranch, true
	case "tag":
		return expr.CtxTag, true
	}
	return "", false
}

func isRef(x expr.Expr, name string) bool {
	r, ok := x.(expr.Ref)
	return ok && len(r.Path) == 1 && r.Path[0] == name
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) expect(k tokKind, what string) (token, error) {
	t := p.peek()
	if t.kind != k {
		return t, syntaxErr(t.pos, "expected %s", what)
	}
	return p.next(), nil
}

type binOp struct {
	op   string
	prec int
}

// Precedence, loosest first: || && (== !=) (< <= > >=); all left-associative.
var binOps = map[tokKind]binOp{
	tOr: {expr.OpOr, 1}, tAnd: {expr.OpAnd, 2},
	tEq: {expr.OpEq, 3}, tNe: {expr.OpNe, 3},
	tLt: {expr.OpLt, 4}, tLe: {expr.OpLe, 4}, tGt: {expr.OpGt, 4}, tGe: {expr.OpGe, 4},
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > maxDepth {
		return syntaxErr(p.peek().pos, "expression nested deeper than %d", maxDepth)
	}
	return nil
}

// nested parses a full expression inside a paren, call or index. Depth counts these levels, '!' included,
// not operator precedence, so a long flat chain of operators costs nothing.
func (p *parser) nested() (expr.Expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	return p.expression(1)
}

func (p *parser) expression(minPrec int) (expr.Expr, error) {
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
		right, err := p.expression(op.prec + 1)
		if err != nil {
			return nil, err
		}
		left = expr.Binary{Op: op.op, L: left, R: right}
	}
}

func (p *parser) unary() (expr.Expr, error) {
	if p.peek().kind != tBang {
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
	case tNum:
		return p.literal(expr.Lit{Kind: expr.LitNum, Num: t.num})
	case tStr:
		return p.literal(expr.Lit{Kind: expr.LitStr, Str: t.text})
	case tLParen:
		e, err := p.nested()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, "')'"); err != nil {
			return nil, err
		}
		return e, p.suffixes(nil)
	case tIdent:
		return p.identStart(t)
	}
	return nil, syntaxErr(t.pos, "unexpected token")
}

// literal marks any index or property access on a literal as unmodelled.
func (p *parser) literal(l expr.Lit) (expr.Expr, error) { return l, p.suffixes(nil) }

func (p *parser) identStart(t token) (expr.Expr, error) {
	if p.peek().kind == tLParen {
		return p.call(t)
	}
	switch t.text {
	case "true", "false":
		return p.literal(expr.Lit{Kind: expr.LitBool, Bool: t.text == "true"})
	case "null":
		return p.literal(expr.Lit{})
	case "NaN", "Infinity":
		p.opaque = true
		return expr.Lit{}, p.suffixes(nil)
	}
	path := []string{t.text}
	if err := p.suffixes(&path); err != nil {
		return nil, err
	}
	if slices.Contains(p.aliases, path[0]) {
		path[0] = githubContext
	}
	return ref(path), nil
}

func (p *parser) call(name token) (expr.Expr, error) {
	p.next()
	var args []expr.Expr
	if p.peek().kind != tRParen {
		for {
			a, err := p.nested()
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			if p.peek().kind != tComma {
				break
			}
			p.next()
		}
	}
	if _, err := p.expect(tRParen, "')' or ','"); err != nil {
		return nil, err
	}
	canon, known := functions[strings.ToLower(name.text)]
	if !known {
		p.opaque = true
		canon = name.text
	}
	return expr.Call{Name: canon, Args: args}, p.suffixes(nil)
}

// suffixes consumes `.name`, `[...]` and `.*`. With path non-nil, `.name` and `['lit']` extend it;
// anything else, or any suffix when path is nil, marks the expression unmodelled.
func (p *parser) suffixes(path *[]string) error {
	for {
		switch p.peek().kind {
		case tDot:
			p.next()
			switch t := p.next(); {
			case t.kind == tIdent && path != nil:
				*path = append(*path, t.text)
			case t.kind == tIdent || t.kind == tStar:
				p.opaque = true
			default:
				return syntaxErr(t.pos, "expected property name after '.'")
			}
		case tLBrack:
			p.next()
			if err := p.indexBody(path); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (p *parser) indexBody(path *[]string) error {
	if p.peek().kind == tStar {
		p.next()
		p.opaque = true
	} else {
		e, err := p.nested()
		if err != nil {
			return err
		}
		if l, ok := e.(expr.Lit); ok && l.Kind == expr.LitStr && path != nil {
			*path = append(*path, l.Str)
		} else {
			p.opaque = true
		}
	}
	_, err := p.expect(tRBrack, "']'")
	return err
}
