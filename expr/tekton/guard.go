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
	envNeeds = "PM_NEEDS_"
	envSplit = "__"
)

// EnvVar is a step env entry that a guard reads: the step sets Name to Value, a Tekton variable.
type EnvVar = shguard.EnvVar

// EnvName gives the step env name that holds a ref. The name inverts only for lowercase dash-separated
// param names, so another name has no env name and no guard.
func EnvName(r expr.Ref) (string, bool) {
	if n, ok := shguard.BaseEnvName(r); ok {
		return n, true
	}
	if len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs" &&
		validTask(r.Path[1]) && dashJobName.MatchString(r.Path[1]) && shguard.DashName.MatchString(r.Path[3]) {
		return envNeeds + shguard.UpperUnder(r.Path[1]) + envSplit + shguard.UpperUnder(r.Path[3]), true
	}
	return "", false
}

// EnvRef inverts EnvName.
func EnvRef(name string) (expr.Ref, bool) {
	if r, ok := shguard.BaseEnvRef(name); ok {
		return r, true
	}
	if rest, ok := strings.CutPrefix(name, envNeeds); ok {
		job, res, found := strings.Cut(rest, envSplit)
		if found && underJob.MatchString(job) && shguard.UnderName.MatchString(res) {
			return expr.Ref{Path: []string{expr.PrefixNeeds, shguard.LowerDash(job), "outputs", shguard.LowerDash(res)}}, true
		}
	}
	return expr.Ref{}, false
}

// noVariable refuses text that Tekton would rewrite: it substitutes $(...) in script text with no escape.
func noVariable(lit string) bool { return !strings.Contains(lit, wordOpen) }

func guardDialect() shguard.Dialect {
	return shguard.Dialect{Sem: Semantics, Name: EnvName, Ref: EnvRef, Literal: noVariable}
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
func GuardEnv(e expr.Expr) ([]EnvVar, bool) { return shguard.EnvVars(e, EnvName, RefWord) }
