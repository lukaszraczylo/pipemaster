package concourse

import "github.com/lukaszraczylo/pipemaster/expr"

// Vocab names the canonical refs that a trigger condition of a Concourse job reads. The condition is built from
// the git resources of the job, so the AST holds canonical refs and each maps to itself.
var Vocab = func() expr.Vocab {
	v := expr.Vocab{ToCanon: map[string][]string{}, FromCanon: map[string][]string{}}
	for _, n := range []string{expr.CtxBranch, expr.CtxTag, expr.CtxEvent, expr.CtxSHA, expr.CtxRepo, expr.CtxDefaultBranch} {
		v.ToCanon[n], v.FromCanon[n] = []string{n}, []string{n}
	}
	for _, fam := range []string{expr.PrefixInputs, expr.PrefixMatrix, expr.PrefixNeeds, expr.PrefixEnv, expr.PrefixVars, expr.PrefixSecrets} {
		v.ToCanon[fam], v.FromCanon[fam] = []string{fam}, []string{fam}
	}
	return v
}()

// Events maps each canonical event to itself: a Concourse condition names the event it was built from.
var Events = map[string]string{"push": "push", "pull_request": "pull_request", "schedule": "schedule", "workflow_dispatch": "workflow_dispatch"}

// Lang is the Concourse dialect for expr.Translate. A trigger condition compares exact strings with no coercion
// and no case folding, which is the GitLab class.
var Lang = expr.Dialect{
	ID:       Dialect,
	Class:    expr.ClassGitLab,
	Vocab:    Vocab,
	Sem:      Semantics,
	Events:   Events,
	Supports: Guardable,
}

func init() { expr.RegisterLang(&Lang) }
