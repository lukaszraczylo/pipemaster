package woodpecker

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const precOpaque = 0

var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Print renders e as an evaluate expression: double-quoted strings, `!`, `&&`, `||`, `matches`, and the
// infix contains, startsWith and endsWith, with minimal parentheses. It errors on nodes expr-lang has no
// form for here: numbers, ordering operators, other calls, refs with no Woodpecker name, a filter glob,
// and strings holding control characters or invalid UTF-8.
func Print(e expr.Expr) (string, error) {
	if o, ok := e.(expr.Opaque); ok {
		return printOpaque(o)
	}
	var sb strings.Builder
	if err := write(&sb, e); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func printOpaque(o expr.Opaque) (string, error) {
	if o.Dialect != Dialect {
		return "", fmt.Errorf("woodpecker expression: cannot print %q opaque expression", o.Dialect)
	}
	if _, _, ok := ParseGlob(o); ok {
		return "", fmt.Errorf("woodpecker expression: a filter glob has no expression form")
	}
	return o.Text, nil
}

func prec(e expr.Expr) int {
	switch v := e.(type) {
	case expr.Binary:
		switch v.Op {
		case expr.OpOr:
			return precOr
		case expr.OpAnd:
			return precAnd
		case expr.OpNoMatch:
			return precNot // printed as !(...)
		}
		return precCmp
	case expr.Call:
		return precCmp
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
		sb.WriteString(name)
		return nil
	case expr.Unary:
		if v.Op != expr.OpNot {
			return fmt.Errorf("woodpecker expression: unary operator %q has no form", v.Op)
		}
		sb.WriteByte('!')
		return operand(sb, v.X, precNot)
	case expr.Binary:
		return writeBinary(sb, v)
	case expr.Call:
		return writeCall(sb, v)
	case expr.Opaque:
		s, err := printOpaque(v)
		if err != nil {
			return err
		}
		sb.WriteString(s)
		return nil
	}
	return fmt.Errorf("woodpecker expression: %T has no form", e)
}

var infix = map[string]struct {
	text string
	prec int
}{
	expr.OpOr: {"||", precOr}, expr.OpAnd: {"&&", precAnd}, expr.OpEq: {"==", precCmp}, expr.OpNe: {"!=", precCmp},
}

func writeBinary(sb *strings.Builder, b expr.Binary) error {
	switch b.Op {
	case expr.OpMatch:
		return writeMatch(sb, b)
	case expr.OpNoMatch:
		sb.WriteString("!(")
		if err := writeMatch(sb, b); err != nil {
			return err
		}
		sb.WriteByte(')')
		return nil
	}
	o, ok := infix[b.Op]
	if !ok {
		return fmt.Errorf("woodpecker expression: operator %q has no form", b.Op)
	}
	if err := operand(sb, b.L, o.prec); err != nil {
		return err
	}
	sb.WriteString(" " + o.text + " ")
	return operand(sb, b.R, o.prec+1)
}

func writeMatch(sb *strings.Builder, b expr.Binary) error {
	pat, ok := b.R.(expr.Lit)
	if !ok || pat.Kind != expr.LitStr {
		return fmt.Errorf("woodpecker expression: matches needs a literal pattern on the right")
	}
	if _, err := regexp.Compile(pat.Str); err != nil {
		return fmt.Errorf("woodpecker expression: pattern %q is not valid RE2: %w", pat.Str, err)
	}
	if err := operand(sb, b.L, precCmp); err != nil {
		return err
	}
	sb.WriteString(" matches ")
	return writeLit(sb, pat)
}

var callWords = map[string]bool{"contains": true, "startsWith": true, "endsWith": true}

func writeCall(sb *strings.Builder, c expr.Call) error {
	if !callWords[c.Name] || len(c.Args) != 2 {
		return fmt.Errorf("woodpecker expression: call %q has no form", c.Name)
	}
	if err := operand(sb, c.Args[0], precCmp); err != nil {
		return err
	}
	sb.WriteString(" " + c.Name + " ")
	return operand(sb, c.Args[1], precCmp+1)
}

var escapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`)

func writeLit(sb *strings.Builder, l expr.Lit) error {
	switch l.Kind {
	case expr.LitNull:
		sb.WriteString("nil")
	case expr.LitBool:
		if l.Bool {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case expr.LitStr:
		if !utf8.ValidString(l.Str) {
			return fmt.Errorf("woodpecker expression: string is not valid UTF-8")
		}
		for i := 0; i < len(l.Str); i++ {
			if c := l.Str[i]; c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				return fmt.Errorf("woodpecker expression: string holds a control character")
			}
		}
		sb.WriteString(`"` + escapes.Replace(l.Str) + `"`)
	default:
		return fmt.Errorf("woodpecker expression: number literals have no form here")
	}
	return nil
}

func varName(path []string) (string, error) {
	if len(path) == 2 {
		name := path[1]
		switch path[0] {
		case expr.PrefixEnv:
			if identRE.MatchString(name) && words[name] == 0 && !strings.HasPrefix(name, nativePrefix) {
				return name, nil
			}
		case Dialect:
			if _, mapped := Vocab.Canon([]string{name}); !mapped && identRE.MatchString(name) &&
				len(name) > len(nativePrefix) && strings.HasPrefix(name, nativePrefix) {
				return name, nil
			}
		}
	}
	if dp, ok := Vocab.Dialect(path); ok && len(dp) == 1 {
		return dp[0], nil
	}
	return "", fmt.Errorf("woodpecker expression: ref %q has no Woodpecker name", strings.Join(path, "."))
}
