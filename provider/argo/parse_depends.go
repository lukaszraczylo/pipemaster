package argo

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Task result atoms of a depends expression; the IR holds the first three.
const (
	atomSucceeded = "Succeeded"
	atomFailed    = "Failed"
	atomSkipped   = "Skipped"
)

var resultOf = map[string]string{atomSucceeded: "success", atomFailed: "failure", atomSkipped: "skipped"}

// otherAtoms are the depends atoms that have no IR status: they make the whole expression unreadable.
var otherAtoms = map[string]bool{"Errored": true, "Omitted": true, "Daemoned": true, "AnySucceeded": true, "AllFailed": true}

// depends is a parsed depends expression.
type depends struct {
	cond  expr.Expr
	tasks []string
	// plain is true when the expression is only task.Succeeded atoms joined by &&.
	plain bool
	// bare is true when an atom names a task with no result, which also passes on Skipped and Daemoned.
	bare bool
	// unreadable is true when an atom has no IR status.
	unreadable bool
}

type dependsParser struct {
	out  *depends
	toks []string
	pos  int
	// neg counts the ! operators around the atom being read.
	neg int
	// onlySucceeded is false once the expression holds anything but && over .Succeeded atoms.
	onlySucceeded bool
}

// parseDepends reads a depends expression: atoms joined by &&, || and !, with parentheses.
func parseDepends(s string) (*depends, error) {
	toks, err := dependsTokens(s)
	if err != nil {
		return nil, err
	}
	p := &dependsParser{toks: toks, out: &depends{}, onlySucceeded: true}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("depends %q: unexpected %q", s, p.toks[p.pos])
	}
	p.out.cond = foldBool(e)
	p.out.plain = p.onlySucceeded && !p.out.bare && !p.out.unreadable
	return p.out, nil
}

func dependsTokens(s string) ([]string, error) {
	var toks []string
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.HasPrefix(s[i:], "&&"), strings.HasPrefix(s[i:], "||"):
			toks = append(toks, s[i:i+2])
			i += 2
		case c == '!' || c == '(' || c == ')':
			toks = append(toks, string(c))
			i++
		case isIdent(c):
			j := i
			for j < len(s) && isIdent(s[j]) {
				j++
			}
			toks = append(toks, s[i:j])
			i = j
		default:
			return nil, fmt.Errorf("depends %q: unexpected character %q", s, c)
		}
	}
	return toks, nil
}

func isIdent(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

func (p *dependsParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *dependsParser) or() (expr.Expr, error) {
	l, err := p.and()
	for err == nil && p.peek() == "||" {
		p.pos++
		p.onlySucceeded = false
		var r expr.Expr
		if r, err = p.and(); err == nil {
			l = expr.Binary{Op: expr.OpOr, L: l, R: r}
		}
	}
	return l, err
}

func (p *dependsParser) and() (expr.Expr, error) {
	l, err := p.unary()
	for err == nil && p.peek() == "&&" {
		p.pos++
		var r expr.Expr
		if r, err = p.unary(); err == nil {
			l = expr.Binary{Op: expr.OpAnd, L: l, R: r}
		}
	}
	return l, err
}

func (p *dependsParser) unary() (expr.Expr, error) {
	switch t := p.peek(); t {
	case "!":
		p.pos++
		p.onlySucceeded = false
		p.neg++
		x, err := p.unary()
		p.neg--
		return expr.Unary{Op: expr.OpNot, X: x}, err
	case "(":
		p.pos++
		e, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("depends: missing )")
		}
		p.pos++
		return e, nil
	case "", ")", "&&", "||":
		return nil, fmt.Errorf("depends: an atom is missing")
	default:
		p.pos++
		return p.atom(t), nil
	}
}

// atom reads task.Result, or a bare task.
func (p *dependsParser) atom(tok string) expr.Expr {
	task, atom := tok, ""
	if i := strings.LastIndex(tok, "."); i > 0 {
		if suffix := tok[i+1:]; resultOf[suffix] != "" || otherAtoms[suffix] {
			task, atom = tok[:i], suffix
		}
	}
	p.out.tasks = appendUnique(p.out.tasks, task)
	switch {
	case atom == "":
		p.out.bare = true
		p.onlySucceeded = false
		return expr.Binary{Op: expr.OpOr, L: resultIs(task, "success"), R: resultIs(task, "skipped")}
	case otherAtoms[atom]:
		p.out.unreadable = true
		p.onlySucceeded = false
		// The atom reads as false, or as true under an odd number of !, so the whole condition only narrows.
		return expr.Lit{Kind: expr.LitBool, Bool: p.neg%2 == 1}
	}
	p.onlySucceeded = p.onlySucceeded && atom == atomSucceeded
	return resultIs(task, resultOf[atom])
}

func appendUnique(l []string, s string) []string {
	for _, e := range l {
		if e == s {
			return l
		}
	}
	return append(l, s)
}

// resultIs is needs.<task>.result == <status>.
func resultIs(task, status string) expr.Expr {
	return expr.Binary{
		Op: expr.OpEq, L: expr.Ref{Path: []string{expr.PrefixNeeds, task, "result"}}, R: expr.Lit{Kind: expr.LitStr, Str: status},
	}
}

// foldBool removes the boolean literals that an atom with no IR status left in the condition.
func foldBool(e expr.Expr) expr.Expr {
	switch v := e.(type) {
	case expr.Unary:
		x := foldBool(v.X)
		if l, ok := x.(expr.Lit); ok && l.Kind == expr.LitBool && v.Op == expr.OpNot {
			return expr.Lit{Kind: expr.LitBool, Bool: !l.Bool}
		}
		return expr.Unary{Op: v.Op, X: x}
	case expr.Binary:
		l, r := foldBool(v.L), foldBool(v.R)
		ll, lok := boolLit(l)
		rl, rok := boolLit(r)
		switch {
		case v.Op == expr.OpAnd && (lok && !ll || rok && !rl):
			return expr.Lit{Kind: expr.LitBool}
		case v.Op == expr.OpOr && (lok && ll || rok && rl):
			return expr.Lit{Kind: expr.LitBool, Bool: true}
		case v.Op == expr.OpAnd && lok:
			return r
		case v.Op == expr.OpAnd && rok:
			return l
		case v.Op == expr.OpOr && lok:
			return r
		case v.Op == expr.OpOr && rok:
			return l
		}
		return expr.Binary{Op: v.Op, L: l, R: r}
	}
	return e
}

func boolLit(e expr.Expr) (value, ok bool) {
	l, isLit := e.(expr.Lit)
	return l.Bool, isLit && l.Kind == expr.LitBool
}
