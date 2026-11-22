package tekton

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
)

// langVocab names every canonical context ref by itself, because the AST that FromWhen builds already holds
// canonical refs. The prefix families carry the user-defined tail.
func langVocab() expr.Vocab {
	v := expr.Vocab{ToCanon: map[string][]string{}, FromCanon: map[string][]string{}}
	for _, n := range ctxparam.Names() {
		path := strings.Split(n, ".")
		v.ToCanon[n], v.FromCanon[n] = path, path
	}
	for _, fam := range []string{expr.PrefixInputs, expr.PrefixMatrix, expr.PrefixNeeds} {
		v.ToCanon[fam], v.FromCanon[fam] = []string{fam}, []string{fam}
	}
	return v
}

// langEvents lists the event literals of a `ctx-event` value: each canonical event stands for itself.
func langEvents() map[string]string {
	return map[string]string{
		"push": "push", "pull_request": "pull_request", "schedule": "schedule", "workflow_dispatch": "workflow_dispatch",
	}
}

// Lang is the Tekton dialect for expr.Translate. A `when` entry compares a variable with a set of exact
// strings (in, notin), with no coercion and no case folding, and an unset param reads as "" and equals no
// non-empty literal. That is the GitLab class: equality of a ref with a non-empty literal under !, && and ||.
// ClassGHA does not fit because it folds case and coerces numbers.
var Lang = expr.Dialect{
	ID:       Dialect,
	Class:    expr.ClassGitLab,
	Vocab:    langVocab(),
	Sem:      Semantics,
	Events:   langEvents(),
	Reads:    inSubset,
	Supports: func(e expr.Expr) bool { _, ok := ToWhen(e); return ok },
}

func init() { expr.RegisterLang(&Lang) }

// inSubset is the `when` expressions that read the same in the GitLab class: equality or inequality of a ref
// with a non-empty string, combined with !, && and ||. A cel entry (Opaque) is outside it.
func inSubset(e expr.Expr) bool {
	switch v := e.(type) {
	case expr.Unary:
		return v.Op == expr.OpNot && inSubset(v.X)
	case expr.Binary:
		switch v.Op {
		case expr.OpAnd, expr.OpOr:
			return inSubset(v.L) && inSubset(v.R)
		case expr.OpEq, expr.OpNe:
			_, isRef := v.L.(expr.Ref)
			lit, isLit := v.R.(expr.Lit)
			return isRef && isLit && lit.Kind == expr.LitStr && lit.Str != ""
		}
	}
	return false
}
