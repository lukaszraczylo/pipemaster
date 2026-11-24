// Package gitlab parses and prints GitLab CI rules:if expressions as canonical expr ASTs.
package gitlab

import "github.com/lukaszraczylo/pipemaster/expr"

// Dialect is the Opaque dialect name and the root of provider-native refs.
const Dialect = "gitlab"

// Semantics is the GitLab rules:if rule set (lib/gitlab/ci/pipeline/expression): == compares same-typed
// values exactly and never coerces null or "", the result and ! use present? (null, false and "" are
// falsy), while the && / || operand test is plain Ruby truthiness (only null and false are falsy).
var Semantics = expr.Semantics{
	StrictTypes:   true,
	Truthy:        present,
	LogicalTruthy: rubyTruthy,
}

func present(v expr.Value) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	}
	return true
}

func rubyTruthy(v expr.Value) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	}
	return true
}

// Vocab maps GitLab predefined variable names (one-segment paths) to canonical refs.
var Vocab = expr.Vocab{
	ToCanon: map[string][]string{
		"CI_COMMIT_BRANCH":                    {expr.CtxBranch},
		"CI_COMMIT_TAG":                       {expr.CtxTag},
		"CI_COMMIT_SHA":                       {expr.CtxSHA},
		"CI_COMMIT_REF_NAME":                  {expr.CtxRefName},
		"CI_PIPELINE_SOURCE":                  {expr.CtxEvent},
		"CI_PROJECT_PATH":                     {expr.CtxRepo},
		"GITLAB_USER_LOGIN":                   {expr.CtxActor},
		"CI_MERGE_REQUEST_IID":                {"pr", "number"},
		"CI_DEFAULT_BRANCH":                   {expr.CtxDefaultBranch},
		"CI_COMMIT_BEFORE_SHA":                {expr.CtxBeforeSHA},
		"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME": {"pr", "head_ref"},
		"CI_MERGE_REQUEST_TARGET_BRANCH_NAME": {"pr", "base_ref"},
	},
	FromCanon: map[string][]string{
		expr.CtxBranch:        {"CI_COMMIT_BRANCH"},
		expr.CtxTag:           {"CI_COMMIT_TAG"},
		expr.CtxSHA:           {"CI_COMMIT_SHA"},
		expr.CtxRefName:       {"CI_COMMIT_REF_NAME"},
		expr.CtxEvent:         {"CI_PIPELINE_SOURCE"},
		expr.CtxRepo:          {"CI_PROJECT_PATH"},
		expr.CtxActor:         {"GITLAB_USER_LOGIN"},
		expr.CtxPRNumber:      {"CI_MERGE_REQUEST_IID"},
		expr.CtxPRHeadRef:     {"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME"},
		expr.CtxPRBaseRef:     {"CI_MERGE_REQUEST_TARGET_BRANCH_NAME"},
		expr.CtxDefaultBranch: {"CI_DEFAULT_BRANCH"},
		expr.CtxBeforeSHA:     {"CI_COMMIT_BEFORE_SHA"},
	},
}

// Events maps canonical (GHA-named) event values to CI_PIPELINE_SOURCE values.
var Events = map[string]string{
	"push":              "push",
	"pull_request":      "merge_request_event",
	"schedule":          "schedule",
	"workflow_dispatch": "web",
}

// ref canonicalises a variable name; an unmapped name stays provider-native under the gitlab root.
func ref(name string) expr.Ref {
	if c, ok := Vocab.Canon([]string{name}); ok {
		return expr.Ref{Path: c}
	}
	return expr.Ref{Path: []string{Dialect, name}}
}

func init() { expr.RegisterLang(&Lang) }

// Lang is the GitLab dialect for expr.Translate.
var Lang = expr.Dialect{
	ID:       Dialect,
	Class:    expr.ClassGitLab,
	Vocab:    Vocab,
	Sem:      Semantics,
	Events:   Events,
	Supports: func(e expr.Expr) bool { _, err := Print(e); return err == nil },
}
