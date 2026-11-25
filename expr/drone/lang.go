package drone

import (
	"maps"
	"slices"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Semantics is how a Drone filter compares: a value equals a literal only when the strings are equal, with no
// coercion and no case folding.
var Semantics = expr.Semantics{StrictTypes: true}

// langVocab is Vocab with the ref type, which the push-only event node tests. Drone has no variable for it, so
// Vocab, which names shell variables, leaves it out.
func langVocab() expr.Vocab {
	v := expr.Vocab{ToCanon: maps.Clone(Vocab.ToCanon), FromCanon: maps.Clone(Vocab.FromCanon)}
	v.FromCanon[expr.CtxRefType] = []string{expr.CtxRefType}
	return v
}

// langEvents lists the event literals of a Drone filter expression. The AST holds canonical names, so each
// canonical event stands for itself; push is the canonical push, which covers a tag push.
func langEvents() map[string]string {
	t := map[string]string{eventPush: eventPush}
	for c := range Events {
		t[c] = c
	}
	return t
}

// Lang is the Drone dialect for expr.Translate: the filter expressions that FromExpr reads and ToExpr writes.
// It shares the GitLab class on the subset that inSubset names, where a ref equals a literal by exact string
// comparison and an unset ref equals no literal.
var Lang = expr.Dialect{
	ID:         Dialect,
	Class:      expr.ClassGitLab,
	BranchOnPR: true,
	TagEvent:   true,
	Vocab:      langVocab(),
	Sem:        Semantics,
	Events:     langEvents(),
	Reads:      inSubset,
	Supports:   func(e expr.Expr) bool { _, ok := FromExpr(e, Semantics); return ok },
}

func init() { expr.RegisterLang(&Lang) }

// langRefs are the refs that a filter expression tests.
var langRefs = [][]string{
	keyRefs[KeyEvent].Path, keyRefs[KeyBranch].Path, keyRefs[KeyRef].Path, keyRefs[KeyRepo].Path, tagRef.Path, refTypeRef.Path,
}

// inSubset is the filter expressions that read the same in the GitLab class: equality of a ref with a non-empty
// string, combined with !, && and ||. A glob (Opaque) and a status call are outside it.
func inSubset(e expr.Expr) bool {
	switch v := e.(type) {
	case expr.Unary:
		return v.Op == expr.OpNot && inSubset(v.X)
	case expr.Binary:
		switch v.Op {
		case expr.OpAnd, expr.OpOr:
			return inSubset(v.L) && inSubset(v.R)
		case expr.OpEq:
			r, isRef := v.L.(expr.Ref)
			lit, isLit := v.R.(expr.Lit)
			return isRef && isLit && lit.Kind == expr.LitStr && lit.Str != "" &&
				slices.ContainsFunc(langRefs, func(p []string) bool { return slices.Equal(p, r.Path) })
		}
	}
	return false
}
