// Package tekton maps Tekton `when` expressions and the shell guard to and from the shared expr AST.
// Tekton has no event context: a reserved `ctx-<name>` pipeline param carries each canonical context value.
package tekton

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
)

// Dialect is the Opaque dialect name.
const Dialect = "tekton"

// Semantics is how a `when` entry and the guard compare: equal strings, no coercion and no case folding.
var Semantics = expr.Semantics{StrictTypes: true}

const (
	paramsHead  = "params."
	tasksHead   = "tasks."
	resultsPart = "results"
	wordOpen    = "$("
	wordClose   = ")"
)

var (
	paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	// dnsLabel is a Kubernetes object or Tekton task name.
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// dashName is a lowercase name whose dash maps to an underscore and back without loss.
	dashName    = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)
	dashJobName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	underName   = regexp.MustCompile(`^[A-Z][A-Z0-9]*(_[A-Z0-9]+)*$`)
	underJob    = regexp.MustCompile(`^[A-Z0-9]+(_[A-Z0-9]+)*$`)
)

const maxLabel = 63

// RefWord writes a canonical ref as the Tekton variable that holds it. A matrix axis is a param inside the
// Task. env, secrets and vars have no word.
func RefWord(r expr.Ref) (string, bool) {
	if p, ok := ctxparam.Param(r.Path); ok {
		return wordOpen + paramsHead + p + wordClose, true
	}
	switch {
	case len(r.Path) == 2 && (r.Path[0] == expr.PrefixInputs || r.Path[0] == expr.PrefixMatrix) && paramName.MatchString(r.Path[1]):
		return wordOpen + paramsHead + r.Path[1] + wordClose, true
	case len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs" && validTask(r.Path[1]) && paramName.MatchString(r.Path[3]):
		return wordOpen + tasksHead + r.Path[1] + "." + resultsPart + "." + r.Path[3] + wordClose, true
	}
	return "", false
}

func validTask(s string) bool { return len(s) <= maxLabel && dnsLabel.MatchString(s) }

// FromWord inverts RefWord for text that is exactly one variable. A param that is not a reserved context
// param reads as an input.
func FromWord(w string) (expr.Ref, bool) {
	inner, ok := strings.CutPrefix(w, wordOpen)
	if !ok {
		return expr.Ref{}, false
	}
	inner, ok = strings.CutSuffix(inner, wordClose)
	if !ok {
		return expr.Ref{}, false
	}
	if name, ok := strings.CutPrefix(inner, paramsHead); ok {
		if !paramName.MatchString(name) {
			return expr.Ref{}, false
		}
		if path, ok := ctxparam.Ref(name); ok {
			return expr.Ref{Path: path}, true
		}
		return expr.Ref{Path: []string{expr.PrefixInputs, name}}, true
	}
	if rest, ok := strings.CutPrefix(inner, tasksHead); ok {
		parts := strings.Split(rest, ".")
		if len(parts) == 3 && parts[1] == resultsPart && validTask(parts[0]) && paramName.MatchString(parts[2]) {
			return expr.Ref{Path: []string{expr.PrefixNeeds, parts[0], "outputs", parts[2]}}, true
		}
	}
	return expr.Ref{}, false
}
