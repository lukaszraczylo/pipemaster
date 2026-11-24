package expr

import "strings"

// Canonical context names.
const (
	CtxBranch   = "branch"
	CtxTag      = "tag"
	CtxEvent    = "event"
	CtxSHA      = "sha"
	CtxRepo     = "repo"
	CtxActor    = "actor"
	CtxPRNumber = "pr.number"
	// CtxRefName is the branch or tag name; CtxBranch and CtxTag are the ref-type-specific views.
	CtxRefName   = "ref_name"
	CtxRefType   = "ref_type"
	CtxRef       = "ref"
	CtxPRHeadRef = "pr.head_ref"
	// CtxDefaultBranch is the default branch of the repository; a dialect without a variable for it cannot name it.
	CtxDefaultBranch = "default_branch"
	// CtxBeforeSHA is the commit a push replaced: all zeros when the push creates the ref.
	CtxBeforeSHA = "before_sha"
	CtxPRBaseRef = "pr.base_ref"
)

// Canonical prefix families; the tail after the prefix is user-defined.
const (
	PrefixMatrix  = "matrix"
	PrefixEnv     = "env"
	PrefixSecrets = "secrets"
	PrefixVars    = "vars"
	PrefixInputs  = "inputs"
	PrefixNeeds   = "needs"
	PrefixSteps   = "steps"
)

var prefixFamilies = map[string]bool{
	PrefixMatrix: true, PrefixEnv: true, PrefixSecrets: true,
	PrefixVars: true, PrefixInputs: true, PrefixNeeds: true, PrefixSteps: true,
}

// Vocab maps one dialect's ref paths, joined with ".", to canonical path segments and back.
type Vocab struct{ ToCanon, FromCanon map[string][]string }

// Canon maps a dialect ref path to its canonical path. A table entry for a prefix family
// (a single canonical segment such as "matrix") maps every deeper path by keeping the tail.
func (v Vocab) Canon(path []string) ([]string, bool) { return lookup(v.ToCanon, path) }

// Dialect maps a canonical path to the dialect path, with the same prefix-family rule as Canon.
func (v Vocab) Dialect(path []string) ([]string, bool) { return lookup(v.FromCanon, path) }

func lookup(table map[string][]string, path []string) ([]string, bool) {
	if len(path) == 0 {
		return nil, false
	}
	if m, ok := table[strings.Join(path, ".")]; ok {
		return append([]string(nil), m...), true
	}
	for i := len(path) - 1; i >= 1; i-- {
		m, ok := table[strings.Join(path[:i], ".")]
		if !ok || !isFamilyMapping(path[:i], m) {
			continue
		}
		out := append(append([]string(nil), m...), path[i:]...)
		return out, true
	}
	return nil, false
}

// isFamilyMapping accepts a prefix mapping only when either side is a lone family name,
// so ordinary leaf entries such as "branch" never swallow a longer path.
func isFamilyMapping(from, to []string) bool {
	return len(from) == 1 && len(to) == 1 && (prefixFamilies[from[0]] || prefixFamilies[to[0]])
}
