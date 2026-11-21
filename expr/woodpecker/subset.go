package woodpecker

import "github.com/lukaszraczylo/pipemaster/expr"

// inSubset is the part of the GitLab class that Woodpecker evaluates identically: regex and equality tests of
// a ref against a non-empty literal, combined with !, && and ||. Every node is boolean-valued and a ref is read
// only inside a test, so an unset name (null in GitLab, "" in Woodpecker) cannot show a difference.
// Mirrors woodpecker.inSubset: lean4/Pipemaster/Woodpecker.lean, GlSub, eval_woodpecker.
func inSubset(e expr.Expr) bool {
	switch v := e.(type) {
	case expr.Unary:
		return v.Op == expr.OpNot && inSubset(v.X)
	case expr.Binary:
		switch v.Op {
		case expr.OpAnd, expr.OpOr:
			return inSubset(v.L) && inSubset(v.R)
		case expr.OpMatch, expr.OpNoMatch:
			return isRefTest(v)
		case expr.OpEq, expr.OpNe:
			return isRefCmp(v)
		}
	}
	return false
}

// isRefTest matches `ref =~ /<shape>/i` where the pattern is one expr.Pattern builds, so it holds a non-empty literal.
func isRefTest(b expr.Binary) bool {
	pat, ok := b.R.(expr.Lit)
	if _, isRef := b.L.(expr.Ref); !isRef || !ok || pat.Kind != expr.LitStr {
		return false
	}
	_, _, ok = expr.ParsePattern(pat.Str)
	return ok
}

// isBareRef is a ref that is the whole condition; an event ref is left out for the reason isRefCmp gives.
func isBareRef(e expr.Expr) bool {
	r, ok := e.(expr.Ref)
	return ok && len(r.Path) == 1 && r.Path[0] != expr.CtxEvent
}

// isNonEmpty matches `ref != ""` as the whole condition: the Woodpecker spelling of the GitLab test of a bare
// ref, which holds for a set, non-empty value (Woodpecker.lean: woodpecker_bare).
func isNonEmpty(e expr.Expr) bool {
	b, ok := e.(expr.Binary)
	lit, isLit := b.R.(expr.Lit)
	return ok && b.Op == expr.OpNe && isLit && lit.Kind == expr.LitStr && lit.Str == "" && isBareRef(b.L)
}

// native spells a bare ref as ref != "", the form expr-lang accepts as a boolean.
func native(e expr.Expr) expr.Expr {
	if isBareRef(e) {
		return expr.Binary{Op: expr.OpNe, L: e, R: expr.Lit{Kind: expr.LitStr}}
	}
	return e
}

// isRefCmp matches `ref == "s"` and `ref != "s"` for a non-empty s. An event ref is left out: its literal maps
// through the Events tables, which the subset proof does not cover for ==.
func isRefCmp(b expr.Binary) bool {
	r, isRef := b.L.(expr.Ref)
	lit, ok := b.R.(expr.Lit)
	return isRef && ok && lit.Kind == expr.LitStr && lit.Str != "" && len(r.Path) == 1 && r.Path[0] != expr.CtxEvent
}

// reads and supports bind the dialect to the subset in both directions: only the subset leaves Woodpecker
// for another dialect, and only the subset arrives from one. A bare ref in the IR stands for ref != "" (see ReadIf).
func reads(e expr.Expr) bool { return isBareRef(e) || inSubset(e) }

// supports also takes ref != "" as the whole condition, the translation of a GitLab bare ref. Under && or || a
// GitLab operand tests for Ruby truthiness, where "" is true, so a bare ref stays out there.
func supports(e expr.Expr) bool {
	if !isNonEmpty(e) && !inSubset(e) {
		return false
	}
	_, err := Print(e)
	return err == nil
}

// ReadIf is the canonical form of a condition read from a Woodpecker file: a whole condition `ref != ""` is the
// bare ref, as in the GitLab class, and a bare name, which expr-lang rejects as non-boolean, stays text.
func ReadIf(e expr.Expr) expr.Expr {
	switch {
	case isNonEmpty(e):
		return e.(expr.Binary).L
	case isBareRef(e):
		text, err := Print(e)
		if err != nil {
			return e
		}
		return expr.Opaque{Dialect: Dialect, Text: text}
	}
	return e
}

// Boolean is the form of a condition that Print writes as expr-lang text: a bare ref reads ref != "".
func Boolean(e expr.Expr) expr.Expr { return native(e) }
