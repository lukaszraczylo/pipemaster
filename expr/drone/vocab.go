// Package drone maps Drone CI step conditions to and from the shared expr AST: structured when filters and
// POSIX shell guards. It registers no expression class, because Drone has no expression language.
package drone

import (
	"regexp"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Dialect is the Opaque dialect name.
const Dialect = "drone"

// Vocab maps Drone built-in variable names (one-segment paths) to canonical refs. Any other name is a user
// env ref; see Ref.
var Vocab = expr.Vocab{
	ToCanon: map[string][]string{
		"DRONE_BRANCH":        {expr.CtxBranch},
		"DRONE_TAG":           {expr.CtxTag},
		"DRONE_BUILD_EVENT":   {expr.CtxEvent},
		"DRONE_COMMIT_SHA":    {expr.CtxSHA},
		"DRONE_REPO":          {expr.CtxRepo},
		"DRONE_COMMIT_AUTHOR": {expr.CtxActor},
		"DRONE_PULL_REQUEST":  {"pr", "number"},
		"DRONE_COMMIT_REF":    {expr.CtxRef},
	},
	FromCanon: map[string][]string{
		expr.CtxBranch:   {"DRONE_BRANCH"},
		expr.CtxTag:      {"DRONE_TAG"},
		expr.CtxEvent:    {"DRONE_BUILD_EVENT"},
		expr.CtxSHA:      {"DRONE_COMMIT_SHA"},
		expr.CtxRepo:     {"DRONE_REPO"},
		expr.CtxActor:    {"DRONE_COMMIT_AUTHOR"},
		expr.CtxPRNumber: {"DRONE_PULL_REQUEST"},
		expr.CtxRef:      {"DRONE_COMMIT_REF"},
	},
}

// Events maps canonical (GHA-named) event values to DRONE_BUILD_EVENT values. Push is absent on purpose: GHA
// calls a tag push a push, Drone reports it as the tag event, so no literal stands for it exactly. The
// remaining Drone events (tag, promote, rollback) have no canonical name.
var Events = map[string]string{
	"pull_request":      "pull_request",
	"schedule":          "cron",
	"workflow_dispatch": "custom",
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Ref canonicalises a variable name: a built-in is its canonical ref and any other name is a user env ref.
func Ref(name string) expr.Ref {
	if c, ok := Vocab.Canon([]string{name}); ok {
		return expr.Ref{Path: c}
	}
	return expr.Ref{Path: []string{expr.PrefixEnv, name}}
}

// VarName inverts Ref: the shell variable that holds r. It fails for a ref with no Drone variable, and for an
// env ref whose name is a built-in or not a shell identifier, since Ref would not read it back.
func VarName(r expr.Ref) (string, bool) {
	if c, ok := Vocab.Dialect(r.Path); ok && len(c) == 1 {
		return c[0], true
	}
	if len(r.Path) != 2 || r.Path[0] != expr.PrefixEnv || !envName.MatchString(r.Path[1]) {
		return "", false
	}
	if _, builtin := Vocab.Canon([]string{r.Path[1]}); builtin {
		return "", false
	}
	return r.Path[1], true
}
