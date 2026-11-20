package woodpecker

import "github.com/lukaszraczylo/pipemaster/expr"

// inSubset is the part of the GitLab class that Woodpecker evaluates identically: regex tests of a ref against
// a non-empty literal, combined with !, && and ||. Every node is boolean-valued and a ref is read only inside
// a regex test, so an unset name (null in GitLab, "" in Woodpecker) cannot show a difference.
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

// reads and supports bind the dialect to the subset in both directions: only the subset leaves Woodpecker
// for another dialect, and only the subset arrives from one.
func reads(e expr.Expr) bool { return inSubset(e) }

func supports(e expr.Expr) bool {
	if !inSubset(e) {
		return false
	}
	_, err := Print(e)
	return err == nil
}
