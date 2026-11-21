// Package drone maps Drone CI step conditions to and from the shared expr AST: structured when filters and
// POSIX shell guards. It registers no expression class, because Drone has no expression language.
package drone

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Dialect is the Opaque dialect name.
const Dialect = "drone"

// Vocab maps Drone built-in variable names (one-segment paths) to canonical refs. Any other name is a user
// env ref; see Ref. Assumption: DRONE_BRANCH is the target branch on a pull request, as the branch filter is
// (research drone.md 4.4); the docs text for the variable is unverified (research drone.md 6).
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

// Events maps canonical (GHA-named) event values to DRONE_BUILD_EVENT values. Push is absent on purpose, as in
// the Woodpecker table: it stands for the set {push, tag} and has no single value (see eventPush). The remaining
// Drone events (tag, promote, rollback) have no canonical name.
var Events = map[string]string{
	"pull_request":      "pull_request",
	"schedule":          "cron",
	"workflow_dispatch": "custom",
}

// nativeEvents are Drone event values with no canonical name; they stand for themselves in the AST.
var nativeEvents = map[string]bool{"tag": true, "promote": true, "rollback": true}

// eventPush is the canonical GHA push event. It covers branch and tag pushes, which Drone splits into the
// events push and tag (research drone.md 4.4, 6), so it stands for the set {push, tag}.
const eventPush = "push"

// droneEvents maps a canonical event literal to the DRONE_BUILD_EVENT values it stands for. A folding source
// compares event names without case and Drone values are lowercase, so the literal is lowered first. A
// literal with no Drone value fails.
func droneEvents(canon string, fold bool) ([]string, bool) {
	if fold {
		canon = strings.ToLower(canon)
	}
	if canon == eventPush {
		return []string{"push", "tag"}, true
	}
	if v, ok := Events[canon]; ok {
		return []string{v}, true
	}
	return []string{canon}, nativeEvents[canon]
}

// canonEvent inverts droneEvents for one DRONE_BUILD_EVENT value, except push, which alone has no canonical
// form: it is push without tag (see pushOnly).
func canonEvent(native string) (string, bool) {
	for c, v := range Events {
		if v == native {
			return c, true
		}
	}
	return native, nativeEvents[native]
}

// cased reports whether s holds a character that case folding changes.
func cased(s string) bool {
	for _, r := range s {
		if unicode.ToLower(r) != r || unicode.ToUpper(r) != r {
			return true
		}
	}
	return false
}

// exactValue reports whether a string compared with == reads the same under sem and under Drone's
// case-sensitive equality: always for an exact source, and for a folding one only when s has no cased character.
func exactValue(s string, sem expr.Semantics) bool { return !sem.CaseInsensitiveEq || !cased(s) }

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

// Option changes how FromExpr, Guardable and PrintGuard read a comparison.
type Option func(*options)

type options struct{ fold bool }

// AllowFold accepts a comparison that a case-folding source writes with a cased literal. Drone compares case
// sensitively, so the result approximates the source and the caller must report it as an emulation.
func AllowFold(o *options) { o.fold = true }

func optsOf(opts []Option) options {
	var o options
	for _, f := range opts {
		f(&o)
	}
	return o
}

// exact reports whether s reads the same on both sides, or the caller accepted the difference.
func (o options) exact(s string, sem expr.Semantics) bool { return o.fold || exactValue(s, sem) }

// headRefPrefix starts the full ref of a branch.
const headRefPrefix = "refs/heads/"

// readRefName rewrites each ref_name comparison with a string literal into the branch-or-tag pair that Drone
// can state: ref_name is the branch name on a branch run and the tag name on a tag run. It applies only
// under AllowFold, because only a source that names ref_name needs it.
func (o options) readRefName(e expr.Expr) expr.Expr {
	if !o.fold {
		return e
	}
	return rewriteRefName(e)
}

func rewriteRefName(e expr.Expr) expr.Expr {
	switch v := e.(type) {
	case expr.Unary:
		v.X = rewriteRefName(v.X)
		return v
	case expr.Binary:
		if v.Op == expr.OpEq || v.Op == expr.OpNe {
			if lit, ok := refNameLiteral(v); ok {
				var pair expr.Expr = expr.Binary{Op: expr.OpOr,
					L: expr.Binary{Op: expr.OpEq, L: keyRefs[KeyRef], R: expr.Lit{Kind: expr.LitStr, Str: headRefPrefix + lit}},
					R: expr.Binary{Op: expr.OpEq, L: tagRef, R: expr.Lit{Kind: expr.LitStr, Str: lit}}}
				if v.Op == expr.OpNe {
					pair = expr.Unary{Op: expr.OpNot, X: pair}
				}
				return pair
			}
		}
		v.L, v.R = rewriteRefName(v.L), rewriteRefName(v.R)
		return v
	}
	return e
}

// refNameLiteral reads ref_name compared with a non-empty string literal, on either side.
func refNameLiteral(b expr.Binary) (string, bool) {
	name, lit := b.L, b.R
	if _, ok := name.(expr.Ref); !ok {
		name, lit = b.R, b.L
	}
	r, isRef := name.(expr.Ref)
	l, isLit := lit.(expr.Lit)
	if !isRef || !isLit || len(r.Path) != 1 || r.Path[0] != expr.CtxRefName || l.Kind != expr.LitStr || l.Str == "" {
		return "", false
	}
	return l.Str, true
}
