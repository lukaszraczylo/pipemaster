package gha

import (
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	refTypeBranch = "branch"
	refTypeTag    = "tag"
)

// emitter carries the state of one Emit call: the ledger, and the lost-text comments waiting for a node.
type emitter struct {
	led      *emitkit.Ledger
	d        Dialect
	comments []string
}

func (e *emitter) lose(path string, f caps.Feature, detail string) {
	e.led.Note(ir.FeatureUse{IRPath: path, Feature: f}, caps.Support{Level: caps.Lost}, detail)
}

func (e *emitter) emulate(path string, f caps.Feature, strategy, detail string) {
	e.led.Note(ir.FeatureUse{IRPath: path, Feature: f}, caps.Support{Level: caps.Emulated, Strategy: strategy}, detail)
}

func (e *emitter) comment(s string) {
	e.comments = append(e.comments, lostPrefix+strings.ReplaceAll(s, "\n", `\n`))
}

// drain returns the pending comment lines and clears them.
func (e *emitter) drain() []string {
	c := e.comments
	e.comments = nil
	return c
}

// ghaRef rewrites the canonical branch and tag refs, which GHA has no names for, into ref_type tests.
func ghaRef(x expr.Expr) expr.Expr {
	switch v := x.(type) {
	case expr.Ref:
		kind := ""
		switch strings.Join(v.Path, ".") {
		case expr.CtxBranch:
			kind = refTypeBranch
		case expr.CtxTag:
			kind = refTypeTag
		default:
			return x
		}
		typed := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxRefType}}, R: expr.Lit{Kind: expr.LitStr, Str: kind}}
		named := expr.Binary{Op: expr.OpAnd, L: typed, R: expr.Ref{Path: []string{expr.CtxRefName}}}
		return expr.Binary{Op: expr.OpOr, L: named, R: expr.Lit{Kind: expr.LitStr}}
	case expr.Unary:
		v.X = ghaRef(v.X)
		return v
	case expr.Binary:
		v.L, v.R = ghaRef(v.L), ghaRef(v.R)
		return v
	case expr.Call:
		args := make([]expr.Expr, len(v.Args))
		for i, a := range v.Args {
			args[i] = ghaRef(a)
		}
		v.Args = args
		return v
	}
	return x
}

func printExpr(x expr.Expr) (string, error) { return exprgha.Print(ghaRef(x)) }

// template converts a canonical script template into GHA text. An expression GHA cannot spell is
// dropped, reported as lost interpolation at path, and kept in a comment.
func (e *emitter) template(path, s string) (string, error) {
	if !strings.Contains(s, "${{") {
		return s, nil
	}
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Expr == nil {
			sb.WriteString(p.Text)
			continue
		}
		text, err := printExpr(p.Expr)
		if err != nil {
			e.lose(path, caps.ScriptInterpolation, err.Error())
			e.comment("script.interpolation " + path + ": ${{ " + expr.String(p.Expr) + " }}")
			continue
		}
		sb.WriteString("${{ " + text + " }}")
	}
	return sb.String(), nil
}

func (e *emitter) templates(path string, ss []string) ([]string, error) {
	out := make([]string, len(ss))
	for i, s := range ss {
		t, err := e.template(path, s)
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}

// expression prints x for a bare slot such as if:; ok is false when GHA cannot spell it.
func (e *emitter) expression(path string, x expr.Expr) (text string, ok bool) {
	text, err := printExpr(x)
	if err != nil {
		e.lose(path, caps.ExprOpaque, err.Error())
		e.comment("expr.opaque " + path + ": " + expr.String(x))
		return "", false
	}
	return text, true
}

// value renders an env or with value; nil means it was lost.
func (e *emitter) value(path string, v ir.Value) (*yaml.Node, error) {
	switch v.Kind {
	case ir.ValueSecret:
		text, err := printExpr(expr.Ref{Path: []string{expr.PrefixSecrets, v.Secret}})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return str("${{ " + text + " }}"), nil
	case ir.ValueExpr:
		return e.exprValue(path, v.Expr), nil
	}
	if !strings.Contains(v.Lit, "${{") {
		return str(v.Lit), nil
	}
	s, err := expr.PrintScriptTemplate([]expr.Part{{Text: v.Lit}})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return str(s), nil
}

func (e *emitter) exprValue(path string, x expr.Expr) *yaml.Node {
	if c, ok := x.(expr.Call); ok {
		if parts, ok := unmixFormat(c); ok {
			if s, ok := joinParts(parts); ok {
				return str(s)
			}
		}
	}
	text, ok := e.expression(path, x)
	if !ok {
		return nil
	}
	return str("${{ " + text + " }}")
}

func joinParts(parts []expr.Part) (string, bool) {
	var sb strings.Builder
	for _, p := range parts {
		if p.Expr == nil {
			sb.WriteString(p.Text)
			continue
		}
		text, err := printExpr(p.Expr)
		if err != nil {
			return "", false
		}
		sb.WriteString("${{ " + text + " }}")
	}
	return sb.String(), true
}

// unmixFormat inverts the parser's mixed(): format('a{0}b{1}', x, y) with sequential placeholders and
// at least two parts is the text a${{ x }}b${{ y }}. Any other format call stays a call.
func unmixFormat(c expr.Call) ([]expr.Part, bool) {
	if c.Name != "format" || len(c.Args) < 2 {
		return nil, false
	}
	pat, ok := c.Args[0].(expr.Lit)
	if !ok || pat.Kind != expr.LitStr {
		return nil, false
	}
	var parts []expr.Part
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			parts = append(parts, expr.Part{Text: text.String()})
			text.Reset()
		}
	}
	next := 0
	s := pat.Str
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "{{"), strings.HasPrefix(s[i:], "}}"):
			text.WriteByte(s[i])
			i++
		case s[i] == '{':
			end := strings.IndexByte(s[i:], '}')
			if end < 0 || s[i+1:i+end] != strconv.Itoa(next) || next+1 >= len(c.Args) {
				return nil, false
			}
			flush()
			parts = append(parts, expr.Part{Expr: c.Args[next+1]})
			next++
			i += end
		case s[i] == '}':
			return nil, false
		default:
			text.WriteByte(s[i])
		}
	}
	flush()
	if next != len(c.Args)-1 || len(parts) < 2 {
		return nil, false
	}
	for _, p := range parts {
		if strings.Contains(p.Text, "${{") {
			return nil, false
		}
	}
	return parts, true
}

// vars renders an env mapping in order; lost values are omitted.
func (e *emitter) vars(path string, vs []ir.Var) (*yaml.Node, error) {
	m := newMap()
	for _, v := range vs {
		n, err := e.value(path, v.Value)
		if err != nil {
			return nil, err
		}
		if n != nil {
			m.set(v.Name, n)
		}
	}
	return m.n, nil
}
