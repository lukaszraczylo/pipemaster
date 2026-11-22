package argo

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
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
)

// dashName is a lowercase name whose dash maps to an underscore and back without loss.
var dashName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

var underName = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$`)

// EnvVar is a container env entry that a guard reads: the container sets Name to Value, an Argo tag.
type EnvVar struct{ Name, Value string }

// EnvName gives the container env name that holds a ref. The name inverts only for lowercase dash-separated
// param names, so another name has no env name and no guard. A needs output has none: the tasks of a DAG are
// not visible inside the template that runs a job.
func EnvName(r expr.Ref) (string, bool) {
	if p, ok := ctxparam.Param(r.Path); ok {
		return envCtx + upperUnder(strings.TrimPrefix(p, ctxparam.Prefix)), true
	}
	if len(r.Path) == 2 && dashName.MatchString(r.Path[1]) {
		switch r.Path[0] {
		case expr.PrefixInputs:
			return envInput + upperUnder(r.Path[1]), true
		case expr.PrefixMatrix:
			return envMatrix + upperUnder(r.Path[1]), true
		}
	}
	return "", false
}

// EnvRef inverts EnvName.
func EnvRef(name string) (expr.Ref, bool) {
	if rest, ok := strings.CutPrefix(name, envCtx); ok && underName.MatchString(rest) {
		path, ok := ctxparam.Ref(ctxparam.Prefix + lowerDash(rest))
		return expr.Ref{Path: path}, ok
	}
	for prefix, family := range map[string]string{envInput: expr.PrefixInputs, envMatrix: expr.PrefixMatrix} {
		if rest, ok := strings.CutPrefix(name, prefix); ok && underName.MatchString(rest) {
			return expr.Ref{Path: []string{family, lowerDash(rest)}}, true
		}
	}
	return expr.Ref{}, false
}

func upperUnder(s string) string { return strings.ToUpper(strings.ReplaceAll(s, "-", "_")) }

func lowerDash(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }

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
			tag, tok := RefTag(v, ScopeTemplate)
			if !nok || !tok {
				ok = false
				return
			}
			if !seen[name] {
				seen[name] = true
				out = append(out, EnvVar{Name: name, Value: tag})
			}
		}
	}
	walk(e)
	if !ok || len(out) == 0 {
		return nil, false
	}
	return out, true
}
