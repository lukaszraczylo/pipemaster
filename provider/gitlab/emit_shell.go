package gitlab

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

const caseFold = "(?i)"

var dqEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`")

// dq quotes s for a shell double-quoted word; "$" stays active so GitLab variables expand.
func dq(s string) string { return `"` + dqEscaper.Replace(s) + `"` }

// dqLit quotes a literal, with "$" escaped so it is not expanded.
func dqLit(s string) string { return `"` + dqEscaper.Replace(strings.ReplaceAll(s, "$", `\$`)) + `"` }

func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// shellWord is the shell form of an operand: a variable expands, a literal does not.
func shellWord(x expr.Expr) (string, bool) {
	switch v := x.(type) {
	case expr.Ref:
		name, ok := refVar(v.Path)
		return `"$` + name + `"`, ok
	case expr.Lit:
		return dqLit(litText(v)), true
	}
	return "", false
}

// shellCond renders a GitLab-semantics condition as a shell command that succeeds when it holds.
func shellCond(x expr.Expr) (string, bool) {
	switch v := x.(type) {
	case expr.Lit:
		if v.Kind == expr.LitBool && v.Bool {
			return "true", true
		}
		if v.Kind == expr.LitStr && v.Str != "" {
			return "true", true
		}
		return "false", true
	case expr.Ref:
		name, ok := refVar(v.Path)
		return `[ -n "$` + name + `" ]`, ok
	case expr.Unary:
		if v.Op != expr.OpNot {
			return "", false
		}
		inner, ok := shellCond(v.X)
		return "! " + groupCmd(inner), ok
	case expr.Binary:
		return shellBinary(v)
	}
	return "", false
}

func groupCmd(s string) string {
	if strings.HasPrefix(s, "[ ") || s == "true" || s == "false" {
		return s
	}
	return "{ " + s + "; }"
}

func shellBinary(b expr.Binary) (string, bool) {
	switch b.Op {
	case expr.OpEq, expr.OpNe:
		l, lok := shellWord(b.L)
		r, rok := shellWord(b.R)
		op := "="
		if b.Op == expr.OpNe {
			op = "!="
		}
		return "[ " + l + " " + op + " " + r + " ]", lok && rok
	case expr.OpMatch, expr.OpNoMatch:
		l, lok := shellWord(b.L)
		pat, pok := b.R.(expr.Lit)
		if !lok || !pok || pat.Kind != expr.LitStr {
			return "", false
		}
		flags := "-Eq"
		p := pat.Str
		if rest, ok := strings.CutPrefix(p, caseFold); ok {
			flags, p = "-Eiq", rest
		}
		s := "printf '%s\\n' " + l + " | grep " + flags + " " + sq(p)
		if b.Op == expr.OpNoMatch {
			s = "! " + s
		}
		return s, true
	case expr.OpAnd, expr.OpOr:
		l, lok := shellCond(b.L)
		r, rok := shellCond(b.R)
		return joinCmd(b.Op, b.L, l) + " " + b.Op + " " + joinCmd(b.Op, b.R, r), lok && rok
	}
	return "", false
}

// joinCmd groups a sub-command unless it is a test or a chain of the same operator.
func joinCmd(op string, x expr.Expr, s string) string {
	if c, ok := x.(expr.Binary); ok && (c.Op == expr.OpAnd || c.Op == expr.OpOr) && c.Op != op {
		return "{ " + s + "; }"
	}
	if u, ok := x.(expr.Unary); ok && u.Op == expr.OpNot {
		return s
	}
	if c, ok := x.(expr.Binary); ok && (c.Op == expr.OpMatch || c.Op == expr.OpNoMatch) {
		return s
	}
	return s
}
