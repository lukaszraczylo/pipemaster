package gitlab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const (
	precOpaque = 0
	caseFlag   = "(?i)"
)

var opPrec = map[string]int{
	expr.OpOr: precOr, expr.OpAnd: precAnd,
	expr.OpEq: precCmp, expr.OpNe: precCmp, expr.OpMatch: precCmp, expr.OpNoMatch: precCmp,
}

// Print renders e as a rules:if expression: double-quoted strings (single-quoted when the text holds a
// double quote), $VAR refs, minimal parentheses. It errors on nodes GitLab cannot express: calls,
// ordering operators, numbers, refs with no GitLab name, a string holding both quote kinds, and a
// pattern GitLab cannot lex (under two characters, trailing backslash).
func Print(e expr.Expr) (string, error) {
	if o, ok := e.(expr.Opaque); ok {
		if o.Dialect != Dialect {
			return "", fmt.Errorf("gitlab expression: cannot print %q opaque expression", o.Dialect)
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
		return precNot
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
		name, err := varName(v.Path)
		if err != nil {
			return err
		}
		sb.WriteString("$" + name)
		return nil
	case expr.Unary:
		if v.Op != expr.OpNot {
			return fmt.Errorf("gitlab expression: unary operator %q has no GitLab form", v.Op)
		}
		sb.WriteByte('!')
		return operand(sb, v.X, precNot)
	case expr.Binary:
		return writeBinary(sb, v)
	case expr.Opaque:
		if v.Dialect != Dialect {
			return fmt.Errorf("gitlab expression: cannot print %q opaque expression", v.Dialect)
		}
		sb.WriteString(v.Text)
		return nil
	}
	return fmt.Errorf("gitlab expression: %T has no GitLab form", e)
}

func writeBinary(sb *strings.Builder, b expr.Binary) error {
	p, ok := opPrec[b.Op]
	if !ok {
		return fmt.Errorf("gitlab expression: operator %q has no GitLab form", b.Op)
	}
	if err := operand(sb, b.L, p); err != nil {
		return err
	}
	sb.WriteString(" " + b.Op + " ")
	if b.Op == expr.OpMatch || b.Op == expr.OpNoMatch {
		pat, ok := b.R.(expr.Lit)
		if !ok || pat.Kind != expr.LitStr {
			return fmt.Errorf("gitlab expression: %s needs a literal pattern on the right", b.Op)
		}
		return writePattern(sb, pat.Str)
	}
	return operand(sb, b.R, p+1)
}

func writeLit(sb *strings.Builder, l expr.Lit) error {
	switch l.Kind {
	case expr.LitNull:
		sb.WriteString("null")
	case expr.LitBool:
		sb.WriteString(strconv.FormatBool(l.Bool))
	case expr.LitStr:
		switch {
		case !strings.Contains(l.Str, `"`):
			sb.WriteString(`"` + l.Str + `"`)
		case !strings.Contains(l.Str, "'"):
			sb.WriteString("'" + l.Str + "'")
		default:
			return fmt.Errorf("gitlab expression: string %q holds both quote kinds and strings have no escapes", l.Str)
		}
	default:
		return fmt.Errorf("gitlab expression: number literals have no GitLab form")
	}
	return nil
}

func writePattern(sb *strings.Builder, pat string) error {
	flags := ""
	if strings.HasPrefix(pat, caseFlag) {
		pat, flags = strings.TrimPrefix(pat, caseFlag), "i"
	}
	if len(pat) < 2 || strings.HasSuffix(pat, `\`) {
		return fmt.Errorf("gitlab expression: pattern %q is too short or ends in a backslash", pat)
	}
	if _, err := regexp.Compile(pat); err != nil {
		return fmt.Errorf("gitlab expression: pattern %q is not valid RE2: %w", pat, err)
	}
	sb.WriteString("/" + strings.ReplaceAll(pat, "/", `\/`) + "/" + flags)
	return nil
}

func varName(path []string) (string, error) {
	switch {
	case len(path) == 0:
		return "", fmt.Errorf("gitlab expression: empty ref")
	case path[0] == Dialect:
		if len(path) != 2 || path[1] == "" || !allWord(path[1]) {
			return "", fmt.Errorf("gitlab expression: ref %q is not a GitLab variable", strings.Join(path, "."))
		}
		return path[1], nil
	}
	if dp, ok := Vocab.Dialect(path); ok && len(dp) == 1 {
		return dp[0], nil
	}
	return "", fmt.Errorf("gitlab expression: ref %q has no GitLab name", strings.Join(path, "."))
}

func allWord(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isWord(s[i]) {
			return false
		}
	}
	return true
}
