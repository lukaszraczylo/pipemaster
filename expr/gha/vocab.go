package gha

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// Semantics is the GitHub Actions evaluation rule set: string comparison ignores case, mismatched
// types coerce to numbers (strings by JSON grammar), and truthiness is the default one.
var Semantics = expr.Semantics{CaseInsensitiveEq: true, JSONNumbers: true}

// Vocab maps GHA context paths to canonical refs. Canonical branch and tag have no GHA name:
// GHA exposes one ref_name for both, so the translator rewrites them before printing.
var Vocab = expr.Vocab{
	ToCanon: map[string][]string{
		"github.ref_name":                  {expr.CtxRefName},
		"github.ref_type":                  {expr.CtxRefType},
		"github.ref":                       {expr.CtxRef},
		"github.sha":                       {expr.CtxSHA},
		"github.event_name":                {expr.CtxEvent},
		"github.repository":                {expr.CtxRepo},
		"github.actor":                     {expr.CtxActor},
		"github.event.pull_request.number": {"pr", "number"},
		"github.head_ref":                  {"pr", "head_ref"},
		"github.base_ref":                  {"pr", "base_ref"},
		expr.PrefixMatrix:                  {expr.PrefixMatrix},
		expr.PrefixEnv:                     {expr.PrefixEnv},
		expr.PrefixSecrets:                 {expr.PrefixSecrets},
		expr.PrefixVars:                    {expr.PrefixVars},
		expr.PrefixInputs:                  {expr.PrefixInputs},
		expr.PrefixNeeds:                   {expr.PrefixNeeds},
		expr.PrefixSteps:                   {expr.PrefixSteps},
	},
	FromCanon: map[string][]string{
		expr.CtxRefName:    {"github", "ref_name"},
		expr.CtxRefType:    {"github", "ref_type"},
		expr.CtxRef:        {"github", "ref"},
		expr.CtxSHA:        {"github", "sha"},
		expr.CtxEvent:      {"github", "event_name"},
		expr.CtxRepo:       {"github", "repository"},
		expr.CtxActor:      {"github", "actor"},
		expr.CtxPRNumber:   {"github", "event", "pull_request", "number"},
		expr.CtxPRHeadRef:  {"github", "head_ref"},
		expr.CtxPRBaseRef:  {"github", "base_ref"},
		expr.PrefixMatrix:  {expr.PrefixMatrix},
		expr.PrefixEnv:     {expr.PrefixEnv},
		expr.PrefixSecrets: {expr.PrefixSecrets},
		expr.PrefixVars:    {expr.PrefixVars},
		expr.PrefixInputs:  {expr.PrefixInputs},
		expr.PrefixNeeds:   {expr.PrefixNeeds},
		expr.PrefixSteps:   {expr.PrefixSteps},
	},
}

// ref canonicalises a GHA path; an unmapped path stays provider-native under the gha root.
func ref(path []string) expr.Ref {
	if c, ok := Vocab.Canon(path); ok {
		return expr.Ref{Path: c}
	}
	return expr.Ref{Path: append([]string{Dialect}, path...)}
}

func isCanonRef(path []string, name string) bool { return strings.Join(path, ".") == name }

// The Gitea and Forgejo expression languages are GHA's with the same vocabulary.
func init() {
	expr.RegisterLang(&Lang)
	for _, id := range []string{provider.Gitea, provider.Forgejo} {
		d := Lang
		d.ID = id
		expr.RegisterLang(&d)
	}
}

// Lang is the GHA dialect for expr.Translate. GHA names events canonically, so its event table is the identity.
var Lang = expr.Dialect{
	ID:    Dialect,
	Class: expr.ClassGHA,
	Vocab: Vocab,
	Sem:   Semantics,
	Events: map[string]string{
		"push": "push", "pull_request": "pull_request", "schedule": "schedule", "workflow_dispatch": "workflow_dispatch",
	},
	Supports: func(e expr.Expr) bool { _, err := Print(e); return err == nil },
}
