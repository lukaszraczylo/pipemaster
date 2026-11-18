package gha

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Binding strength used to parenthesise minimally; a nested Opaque has unknown strength and is wrapped.
const (
	precOpaque = 0
	precUnary  = 5
	precAtom   = 6
)

var opPrec = map[string]int{
	expr.OpOr: 1, expr.OpAnd: 2, expr.OpEq: 3, expr.OpNe: 3, expr.OpLt: 4, expr.OpLe: 4, expr.OpGt: 4, expr.OpGe: 4,
}

// Print renders e as a GHA expression (no ${{ }}), single-quoted strings, minimal parentheses.
// It errors on nodes GHA cannot express: =~ and !~, refs with no GHA name, refs of another provider,
// unknown functions, non-finite numbers and Opaque of another dialect.
func Print(e expr.Expr) (string, error) {
	if o, ok := e.(expr.Opaque); ok {
		if o.Dialect != Dialect {
			return "", fmt.Errorf("gha expression: cannot print %q opaque expression", o.Dialect)
		}
		return o.Text, nil
	}
	var sb strings.Builder
	if err := write(&sb, e); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func prec(e expr.Expr) int {
	switch v := e.(type) {
	case expr.Binary:
		return opPrec[v.Op]
	case expr.Unary:
		return precUnary
	case expr.Opaque:
		return precOpaque
	}
	return precAtom
}

func operand(sb *strings.Builder, e expr.Expr, floor int) error {
	if prec(e) >= floor {
		return write(sb, e)
	}
	sb.WriteByte('(')
	if err := write(sb, e); err != nil {
		return err
	}
	sb.WriteByte(')')
	return nil
}

func write(sb *strings.Builder, e expr.Expr) error {
	switch v := e.(type) {
	case expr.Lit:
		return writeLit(sb, v)
	case expr.Ref:
		return writeRef(sb, v.Path)
	case expr.Unary:
		if v.Op != expr.OpNot {
			return fmt.Errorf("gha expression: unary operator %q has no GHA form", v.Op)
		}
		sb.WriteByte('!')
		return operand(sb, v.X, precUnary)
	case expr.Binary:
		p, ok := opPrec[v.Op]
		if !ok {
			return fmt.Errorf("gha expression: operator %q has no GHA form", v.Op)
		}
		if err := operand(sb, v.L, p); err != nil {
			return err
		}
		sb.WriteString(" " + v.Op + " ")
		return operand(sb, v.R, p+1)
	case expr.Call:
		return writeCall(sb, v)
	case expr.Opaque:
		if v.Dialect != Dialect {
			return fmt.Errorf("gha expression: cannot print %q opaque expression", v.Dialect)
		}
		sb.WriteString(v.Text)
		return nil
	}
	return fmt.Errorf("gha expression: unsupported node %T", e)
}

func writeCall(sb *strings.Builder, c expr.Call) error {
	if functions[strings.ToLower(c.Name)] != c.Name {
		return fmt.Errorf("gha expression: unknown function %q", c.Name)
	}
	sb.WriteString(c.Name + "(")
	for i, a := range c.Args {
		if i > 0 {
			sb.WriteString(", ")
		}
		if err := write(sb, a); err != nil {
			return err
		}
	}
	sb.WriteByte(')')
	return nil
}

func writeLit(sb *strings.Builder, l expr.Lit) error {
	switch l.Kind {
	case expr.LitNull:
		sb.WriteString("null")
	case expr.LitBool:
		sb.WriteString(strconv.FormatBool(l.Bool))
	case expr.LitNum:
		if math.IsNaN(l.Num) || math.IsInf(l.Num, 0) {
			return fmt.Errorf("gha expression: non-finite number has no GHA literal")
		}
		sb.WriteString(strconv.FormatFloat(l.Num, 'g', -1, 64))
	case expr.LitStr:
		sb.WriteString(expr.QuoteSingle(l.Str))
	default:
		return fmt.Errorf("gha expression: unknown literal kind %d", l.Kind)
	}
	return nil
}

func writeRef(sb *strings.Builder, path []string) error {
	dp, err := dialectPath(path)
	if err != nil {
		return err
	}
	first := dp[0]
	if !expr.IsIdent(first) || first == "true" || first == "false" || first == "null" || first == "NaN" || first == "Infinity" {
		return fmt.Errorf("gha expression: ref root %q is not a GHA context name", first)
	}
	sb.WriteString(first)
	for _, seg := range dp[1:] {
		if expr.IsIdent(seg) {
			sb.WriteString("." + seg)
		} else {
			sb.WriteString("[" + expr.QuoteSingle(seg) + "]")
		}
	}
	return nil
}

func dialectPath(path []string) ([]string, error) {
	switch {
	case len(path) == 0:
		return nil, fmt.Errorf("gha expression: empty ref")
	case path[0] == Dialect:
		if len(path) == 1 {
			return nil, fmt.Errorf("gha expression: ref %q has no GHA name", Dialect)
		}
		return path[1:], nil
	case isCanonRef(path, expr.CtxBranch), isCanonRef(path, expr.CtxTag):
		return nil, fmt.Errorf("gha expression: canonical ref %q has no GHA name; rewrite it to ref_name/ref_type first", strings.Join(path, "."))
	}
	if dp, ok := Vocab.Dialect(path); ok {
		return dp, nil
	}
	return nil, fmt.Errorf("gha expression: ref %q has no GHA name", strings.Join(path, "."))
}
