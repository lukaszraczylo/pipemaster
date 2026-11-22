package shguard

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
)

// Env name prefixes of the refs that every provider can read in a step.
const (
	EnvCtx    = "PM_CTX_"
	EnvInput  = "PM_INPUT_"
	EnvMatrix = "PM_MATRIX_"
)

var (
	// DashName is a lowercase name whose dash maps to an underscore and back without loss.
	DashName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	// UnderName is the env spelling of a DashName.
	UnderName = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$`)
)

// EnvVar is a step env entry that a guard reads: the step sets Name to Value, a provider variable.
type EnvVar struct{ Name, Value string }

// UpperUnder spells a dash name as an env name.
func UpperUnder(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

// LowerDash inverts UpperUnder.
func LowerDash(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }

// BaseEnvName gives the env name of a context, input or matrix ref. The name inverts only for lowercase
// dash-separated names, so another name has none.
func BaseEnvName(r expr.Ref) (string, bool) {
	if p, ok := ctxparam.Param(r.Path); ok {
		return EnvCtx + UpperUnder(strings.TrimPrefix(p, ctxparam.Prefix)), true
	}
	if len(r.Path) == 2 && DashName.MatchString(r.Path[1]) {
		switch r.Path[0] {
		case expr.PrefixInputs:
			return EnvInput + UpperUnder(r.Path[1]), true
		case expr.PrefixMatrix:
			return EnvMatrix + UpperUnder(r.Path[1]), true
		}
	}
	return "", false
}

// BaseEnvRef inverts BaseEnvName.
func BaseEnvRef(name string) (expr.Ref, bool) {
	if rest, ok := strings.CutPrefix(name, EnvCtx); ok && UnderName.MatchString(rest) {
		path, ok := ctxparam.Ref(ctxparam.Prefix + LowerDash(rest))
		return expr.Ref{Path: path}, ok
	}
	for prefix, family := range map[string]string{EnvInput: expr.PrefixInputs, EnvMatrix: expr.PrefixMatrix} {
		if rest, ok := strings.CutPrefix(name, prefix); ok && UnderName.MatchString(rest) {
			return expr.Ref{Path: []string{family, LowerDash(rest)}}, true
		}
	}
	return expr.Ref{}, false
}

// EnvVars lists the env entries that the guard for e reads, in order of first use: name gives each ref its
// env name and word gives the provider variable that holds it. It fails when a ref has no name or no word,
// or when e holds no ref.
func EnvVars(e expr.Expr, name, word func(expr.Ref) (string, bool)) ([]EnvVar, bool) {
	var out []EnvVar
	seen := map[string]bool{}
	ok := true
	var walk func(expr.Expr)
	walk = func(e expr.Expr) {
		switch v := e.(type) {
		case expr.Unary:
			walk(v.X)
		case expr.Binary:
			walk(v.L)
			walk(v.R)
		case expr.Ref:
			n, nok := name(v)
			w, wok := word(v)
			if !nok || !wok {
				ok = false
				return
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, EnvVar{Name: n, Value: w})
			}
		}
	}
	walk(e)
	if !ok || len(out) == 0 {
		return nil, false
	}
	return out, true
}
