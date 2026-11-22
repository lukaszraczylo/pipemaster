package argo

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/internal/shguard"
)

// GuardMarker ends a guard line so ParseGuard can tell it from a script line that the author wrote.
const GuardMarker = shguard.Marker

// ErrNotGuardable marks a condition that has no POSIX test form here.
var ErrNotGuardable = shguard.ErrNotGuardable

// EnvVar is a container env entry that a guard reads: the container sets Name to Value, an Argo tag.
type EnvVar = shguard.EnvVar

// EnvName gives the container env name that holds a ref. The name inverts only for lowercase dash-separated
// param names, so another name has no env name and no guard. A needs output has none: the tasks of a DAG are
// not visible inside the template that runs a job.
func EnvName(r expr.Ref) (string, bool) { return shguard.BaseEnvName(r) }

// EnvRef inverts EnvName.
func EnvRef(name string) (expr.Ref, bool) { return shguard.BaseEnvRef(name) }

// noTag refuses text that Argo would rewrite: it substitutes {{...}} in args and script text with no escape.
func noTag(lit string) bool { return !strings.Contains(lit, tagOpen) }

func guardDialect() shguard.Dialect {
	return shguard.Dialect{Sem: Semantics, Name: EnvName, Ref: EnvRef, Literal: noTag}
}

// Guardable reports whether PrintGuard accepts e.
func Guardable(e expr.Expr) bool { return guardDialect().Guardable(e) }

// PrintGuard writes the script line that skips the rest of a step when e is false. Each ref reads from a
// container env entry (see GuardEnv), never from `{{workflow.parameters.x}}` text in the script, because Argo
// substitutes that text with no escaping.
func PrintGuard(e expr.Expr) (string, error) { return guardDialect().Print(e) }

// ParseGuard inverts PrintGuard. It accepts only text that PrintGuard writes.
func ParseGuard(line string) (expr.Expr, bool) { return guardDialect().Parse(line) }

// AddGuard puts the guard for e before the script lines.
func AddGuard(script []string, e expr.Expr) ([]string, error) { return guardDialect().Add(script, e) }

// RemoveGuard strips a leading guard line and returns its condition.
func RemoveGuard(script []string) ([]string, expr.Expr, bool) { return guardDialect().Remove(script) }

// GuardEnv lists the container env entries that the guard for e reads, in order of first use. It fails when
// a ref has no env name or e holds no ref.
func GuardEnv(e expr.Expr) ([]EnvVar, bool) {
	return shguard.EnvVars(e, EnvName, func(r expr.Ref) (string, bool) { return RefTag(r, ScopeTemplate) })
}
