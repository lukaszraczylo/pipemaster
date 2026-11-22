// Package concourse maps the shared shell guard of a Concourse script task to and from the expr AST. Concourse has
// no expression language and no event context, so a condition reads shell variables that the task script fills
// from the git resource files and from the task params.
package concourse

import (
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/internal/shguard"
)

// Dialect is the Opaque dialect name.
const Dialect = "concourse"

// Semantics is how the shell test compares: equal strings, no coercion and no case folding.
var Semantics = expr.Semantics{StrictTypes: true}

// GuardMarker ends a guard line so ParseGuard can tell it from a script line that the author wrote.
const GuardMarker = shguard.Marker

// ErrNotGuardable marks a condition that has no POSIX test form here.
var ErrNotGuardable = shguard.ErrNotGuardable

// EnvVar is a shell variable that a guard reads: Name is the variable and Value says where the task gets it.
type EnvVar = shguard.EnvVar

// EnvName gives the shell variable that holds a ref. The name inverts only for lowercase dash-separated names, so
// any other ref has no variable and no guard.
func EnvName(r expr.Ref) (string, bool) { return shguard.BaseEnvName(r) }

// EnvRef inverts EnvName.
func EnvRef(name string) (expr.Ref, bool) { return shguard.BaseEnvRef(name) }

func guardDialect() shguard.Dialect {
	return shguard.Dialect{Sem: Semantics, Name: EnvName, Ref: EnvRef}
}

// Guardable reports whether PrintGuard accepts e.
func Guardable(e expr.Expr) bool { return guardDialect().Guardable(e) }

// PrintGuard writes the script line that ends the script with success when e is false.
func PrintGuard(e expr.Expr) (string, error) { return guardDialect().Print(e) }

// ParseGuard inverts PrintGuard. It accepts only text that PrintGuard writes.
func ParseGuard(line string) (expr.Expr, bool) { return guardDialect().Parse(line) }

// AddGuard puts the guard for e before the script lines.
func AddGuard(script []string, e expr.Expr) ([]string, error) { return guardDialect().Add(script, e) }

// RemoveGuard strips a leading guard line and returns its condition.
func RemoveGuard(script []string) ([]string, expr.Expr, bool) { return guardDialect().Remove(script) }

// GuardRefs lists the refs of e in order of first use, or false when a ref has no variable name or e has none.
func GuardRefs(e expr.Expr) ([]expr.Ref, bool) {
	vars, ok := shguard.EnvVars(e, EnvName, func(expr.Ref) (string, bool) { return "", true })
	if !ok {
		return nil, false
	}
	out := make([]expr.Ref, len(vars))
	for i, v := range vars {
		r, ok := EnvRef(v.Name)
		if !ok {
			return nil, false
		}
		out[i] = r
	}
	return out, true
}
