package expr

import (
	"crypto/sha256"
	"encoding/hex"
)

// ChangesCall names the canonical path predicate: changes('a', 'b') is true when any changed file matches
// any glob. It is how a GitLab rules:changes list enters an IR condition; a dialect with no per-job path
// filter does not translate it, so the condition stays unexpressible there.
const ChangesCall = "changes"

// Changes builds the path predicate over globs.
func Changes(globs ...string) Expr {
	args := make([]Expr, len(globs))
	for i, g := range globs {
		args[i] = Lit{Kind: LitStr, Str: g}
	}
	return Call{Name: ChangesCall, Args: args}
}

// ChangesGlobs returns the globs of a path predicate; ok is false for any other node.
func ChangesGlobs(e Expr) ([]string, bool) {
	c, ok := e.(Call)
	if !ok || c.Name != ChangesCall || len(c.Args) == 0 {
		return nil, false
	}
	out := make([]string, len(c.Args))
	for i, a := range c.Args {
		s, sok := isStr(a)
		if !sok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// HasChanges reports whether e holds a path predicate.
func HasChanges(e Expr) bool { return len(ChangesAtoms(e)) > 0 }

// ChangesAtoms lists the distinct path predicates of e in order of first use.
func ChangesAtoms(e Expr) []Expr {
	var out []Expr
	seen := map[string]bool{}
	var walk func(Expr)
	walk = func(x Expr) {
		switch v := x.(type) {
		case Unary:
			walk(v.X)
		case Binary:
			walk(v.L)
			walk(v.R)
		case Call:
			if _, ok := ChangesGlobs(v); ok {
				if k := String(v); !seen[k] {
					seen[k] = true
					out = append(out, v)
				}
				return
			}
			for _, a := range v.Args {
				walk(a)
			}
		}
	}
	walk(e)
	return out
}

// AssignChanges replaces each path predicate named in vals (by its canonical text) with a boolean and folds
// the connectives that the constant decides. Truthiness is all it preserves, which is all a condition needs.
func AssignChanges(e Expr, vals map[string]bool) Expr {
	switch v := e.(type) {
	case Unary:
		x := AssignChanges(v.X, vals)
		if l, ok := boolLit(x); ok && v.Op == OpNot {
			return Lit{Kind: LitBool, Bool: !l}
		}
		v.X = x
		return v
	case Binary:
		l, r := AssignChanges(v.L, vals), AssignChanges(v.R, vals)
		if v.Op == OpAnd || v.Op == OpOr {
			return foldJoin(v.Op, l, r)
		}
		v.L, v.R = l, r
		return v
	case Call:
		if _, ok := ChangesGlobs(v); ok {
			if b, set := vals[String(v)]; set {
				return Lit{Kind: LitBool, Bool: b}
			}
		}
	}
	return e
}

func boolLit(e Expr) (bool, bool) {
	l, ok := e.(Lit)
	return l.Bool, ok && l.Kind == LitBool
}

func foldJoin(op string, l, r Expr) Expr {
	absorb := op == OpOr
	if b, ok := boolLit(l); ok {
		if b == absorb {
			return Lit{Kind: LitBool, Bool: absorb}
		}
		return r
	}
	if b, ok := boolLit(r); ok {
		if b == absorb {
			return Lit{Kind: LitBool, Bool: absorb}
		}
		return l
	}
	return Binary{Op: op, L: l, R: r}
}

// ChangesOut names the shell value of a path predicate: c and eight hex digits of the canonical text.
func ChangesOut(a Expr) string {
	sum := sha256.Sum256([]byte(String(a)))
	return "c" + hex.EncodeToString(sum[:4])
}
