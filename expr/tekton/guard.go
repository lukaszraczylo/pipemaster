package tekton

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/internal/shguard"
)

// GuardMarker ends a guard line so ParseGuard can tell it from a script line that the author wrote.
const GuardMarker = shguard.Marker

// ErrNotGuardable marks a condition that has no POSIX test form here.
var ErrNotGuardable = shguard.ErrNotGuardable

const (
	envCtx    = "PM_CTX_"
	envInput  = "PM_INPUT_"
	envMatrix = "PM_MATRIX_"
	envNeeds  = "PM_NEEDS_"
	envSplit  = "__"
)

// EnvVar is a step env entry that a guard reads: the step sets Name to Value, a Tekton variable.
type EnvVar struct{ Name, Value string }

// EnvName gives the step env name that holds a ref. The name inverts only for lowercase dash-separated
// param names, so another name has no env name and no guard.
func EnvName(r expr.Ref) (string, bool) {
	if p, ok := CtxParam(r.Path); ok {
		return envCtx + upperUnder(strings.TrimPrefix(p, ctxPrefix)), true
	}
	switch {
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs && dashName.MatchString(r.Path[1]):
		return envInput + upperUnder(r.Path[1]), true
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix && dashName.MatchString(r.Path[1]):
		return envMatrix + upperUnder(r.Path[1]), true
	case len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs" &&
		validTask(r.Path[1]) && dashJobName.MatchString(r.Path[1]) && dashName.MatchString(r.Path[3]):
		return envNeeds + upperUnder(r.Path[1]) + envSplit + upperUnder(r.Path[3]), true
	}
	return "", false
}

// EnvRef inverts EnvName.
func EnvRef(name string) (expr.Ref, bool) {
	if rest, ok := strings.CutPrefix(name, envCtx); ok && underName.MatchString(rest) {
		path, ok := CtxRef(ctxPrefix + lowerDash(rest))
		return expr.Ref{Path: path}, ok
	}
	for prefix, family := range map[string]string{envInput: expr.PrefixInputs, envMatrix: expr.PrefixMatrix} {
		if rest, ok := strings.CutPrefix(name, prefix); ok && underName.MatchString(rest) {
			return expr.Ref{Path: []string{family, lowerDash(rest)}}, true
		}
	}
	if rest, ok := strings.CutPrefix(name, envNeeds); ok {
		job, res, found := strings.Cut(rest, envSplit)
		if found && underJob.MatchString(job) && underName.MatchString(res) {
			return expr.Ref{Path: []string{expr.PrefixNeeds, lowerDash(job), "outputs", lowerDash(res)}}, true
		}
	}
	return expr.Ref{}, false
}

func upperUnder(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

func lowerDash(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }

func guardDialect() shguard.Dialect {
	return shguard.Dialect{Sem: Semantics, Name: EnvName, Ref: EnvRef}
}

// Guardable reports whether PrintGuard accepts e.
func Guardable(e expr.Expr) bool { return guardDialect().Guardable(e) }

// PrintGuard writes the script line that skips the rest of a step when e is false. Each ref reads from a step
// env entry (see GuardEnv), never from `$(params.x)` text in the script, because Tekton substitutes that text
// with no escaping.
func PrintGuard(e expr.Expr) (string, error) { return guardDialect().Print(e) }

// ParseGuard inverts PrintGuard. It accepts only text that PrintGuard writes.
func ParseGuard(line string) (expr.Expr, bool) { return guardDialect().Parse(line) }

// AddGuard puts the guard for e before the script lines.
func AddGuard(script []string, e expr.Expr) ([]string, error) { return guardDialect().Add(script, e) }

// RemoveGuard strips a leading guard line and returns its condition.
func RemoveGuard(script []string) ([]string, expr.Expr, bool) { return guardDialect().Remove(script) }

// GuardEnv lists the step env entries that the guard for e reads, in order of first use. It fails when a ref
// has no env name or e holds no ref.
func GuardEnv(e expr.Expr) ([]EnvVar, bool) {
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
			name, nok := EnvName(v)
			word, wok := RefWord(v)
			if !nok || !wok {
				ok = false
				return
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, EnvVar{Name: name, Value: word})
			}
		}
	}
	walk(e)
	if !ok || len(out) == 0 {
		return nil, false
	}
	return out, true
}
