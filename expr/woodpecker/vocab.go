// Package woodpecker parses and prints Woodpecker CI evaluate expressions (expr-lang) as canonical expr ASTs
// and maps structured when filters to and from them.
package woodpecker

import "github.com/lukaszraczylo/pipemaster/expr"

// Dialect is the Opaque dialect name and the root of provider-native refs.
const Dialect = "woodpecker"

// nativePrefix starts every built-in Woodpecker variable; any other name in evaluate is a user env ref.
const nativePrefix = "CI_"

// Semantics is expr-lang over a map[string]string environment (pipeline/frontend/yaml/constraint,
// v3.20.1): == compares same-typed values and never coerces. Eval cannot model the compile errors expr-lang
// raises for a non-boolean operand of !, && and ||, so only the comparison rules are encoded here.
var Semantics = expr.Semantics{StrictTypes: true}

// UnsetEnv reads a ref the wrapped env lacks as "": expr-lang over map[string]string yields the zero value
// for a missing key, so an unset name equals "" and differs from nil (lean4/Pipemaster/Woodpecker.lean).
type UnsetEnv struct{ expr.Env }

// Lookup returns "" for an unset or null ref.
func (u UnsetEnv) Lookup(path []string) (expr.Value, bool) {
	if v, ok := u.Env.Lookup(path); ok && v != nil {
		return v, true
	}
	return "", true
}

// Vocab maps Woodpecker built-in variable names (one-segment paths) to canonical refs. CI_COMMIT_REF is the
// full ref (refs/heads/x), the canonical "ref", not the short ref_name, which Woodpecker has no variable for.
var Vocab = expr.Vocab{
	ToCanon: map[string][]string{
		"CI_COMMIT_BRANCH":        {expr.CtxBranch},
		"CI_COMMIT_TAG":           {expr.CtxTag},
		"CI_PIPELINE_EVENT":       {expr.CtxEvent},
		"CI_COMMIT_SHA":           {expr.CtxSHA},
		"CI_REPO":                 {expr.CtxRepo},
		"CI_COMMIT_AUTHOR":        {expr.CtxActor},
		"CI_COMMIT_PULL_REQUEST":  {"pr", "number"},
		"CI_COMMIT_REF":           {expr.CtxRef},
		"CI_COMMIT_SOURCE_BRANCH": {"pr", "head_ref"},
		"CI_COMMIT_TARGET_BRANCH": {"pr", "base_ref"},
	},
	FromCanon: map[string][]string{
		expr.CtxBranch:    {"CI_COMMIT_BRANCH"},
		expr.CtxTag:       {"CI_COMMIT_TAG"},
		expr.CtxEvent:     {"CI_PIPELINE_EVENT"},
		expr.CtxSHA:       {"CI_COMMIT_SHA"},
		expr.CtxRepo:      {"CI_REPO"},
		expr.CtxActor:     {"CI_COMMIT_AUTHOR"},
		expr.CtxPRNumber:  {"CI_COMMIT_PULL_REQUEST"},
		expr.CtxRef:       {"CI_COMMIT_REF"},
		expr.CtxPRHeadRef: {"CI_COMMIT_SOURCE_BRANCH"},
		expr.CtxPRBaseRef: {"CI_COMMIT_TARGET_BRANCH"},
	},
}

// Events maps the canonical (GHA-named) event values the canonical table holds to CI_PIPELINE_EVENT values.
// Woodpecker events with no canonical name (tag, deployment, release, pull_request_closed,
// pull_request_metadata) stay unmapped.
var Events = map[string]string{
	"push":              "push",
	"pull_request":      "pull_request",
	"schedule":          "cron",
	"workflow_dispatch": "manual",
}

// ref canonicalises a variable name: a mapped built-in is canonical, an unmapped CI_ name stays native under
// the woodpecker root, and every other name is a user env ref.
func ref(name string) expr.Ref {
	if c, ok := Vocab.Canon([]string{name}); ok {
		return expr.Ref{Path: c}
	}
	if len(name) > len(nativePrefix) && name[:len(nativePrefix)] == nativePrefix {
		return expr.Ref{Path: []string{Dialect, name}}
	}
	return expr.Ref{Path: []string{expr.PrefixEnv, name}}
}

func init() { expr.RegisterLang(&Lang) }

// Lang is the Woodpecker dialect. Its class translates nothing: expr.Translate refuses every pair with it.
var Lang = expr.Dialect{
	ID:       Dialect,
	Class:    expr.ClassWoodpecker,
	Vocab:    Vocab,
	Sem:      Semantics,
	Events:   Events,
	Supports: func(e expr.Expr) bool { _, err := Print(e); return err == nil },
}
