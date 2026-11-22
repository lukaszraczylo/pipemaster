// Package argo maps Argo Workflows `when` expressions and the shell guard to and from the shared expr AST.
// Argo has no event context: a reserved `ctx-<name>` workflow parameter carries each canonical context value.
package argo

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
)

// Dialect is the Opaque dialect name.
const Dialect = "argo"

// Semantics is how a `when` expression compares: equal strings, no coercion and no case folding, as the
// expr-lang `==` does on a string and a number or on two strings.
var Semantics = expr.Semantics{StrictTypes: true}

// Scope is where a reference is read. A DAG task sees its loop item and the other tasks; the template that
// the task runs sees only its own inputs, so a matrix axis is a template input there.
type Scope uint8

// Reference scopes.
const (
	ScopeTask Scope = iota
	ScopeTemplate
)

const (
	tagOpen  = "{{"
	tagClose = "}}"

	loopItem  = "item"
	tasksHead = "tasks"
	// jsonParam is the all-parameters list, which a simple tag cannot tell from a parameter named json.
	jsonParam = "json"
)

var (
	paramName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	// dnsLabel is a Kubernetes object or Argo task name.
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

const maxLabel = 63

func validTask(s string) bool { return len(s) <= maxLabel && dnsLabel.MatchString(s) }

// part is one step of a member path: a fixed word, or a name that the user chose.
type part struct {
	text string
	name bool
}

func word(s ...string) []part {
	out := make([]part, len(s))
	for i, w := range s {
		out[i] = part{text: w}
	}
	return out
}

func named(s string) part { return part{text: s, name: true} }

// segments gives the member path that holds a canonical ref in a scope.
func segments(r expr.Ref, s Scope) ([]part, bool) {
	if p, ok := ctxparam.Param(r.Path); ok {
		return append(word("workflow", "parameters"), named(p)), true
	}
	switch {
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs && paramName.MatchString(r.Path[1]):
		return append(word("workflow", "parameters"), named(r.Path[1])), true
	case len(r.Path) == 2 && r.Path[0] == expr.PrefixMatrix && paramName.MatchString(r.Path[1]):
		if s == ScopeTemplate {
			return append(word("inputs", "parameters"), named(r.Path[1])), true
		}
		return append(word(loopItem), named(r.Path[1])), true
	case len(r.Path) == 4 && r.Path[0] == expr.PrefixNeeds && r.Path[2] == "outputs" &&
		validTask(r.Path[1]) && paramName.MatchString(r.Path[3]) && s == ScopeTask:
		return append(append(word(tasksHead), named(r.Path[1])), append(word("outputs", "parameters"), named(r.Path[3]))...), true
	}
	return nil, false
}

// fromSegments inverts segments.
func fromSegments(seg []string, s Scope) (expr.Ref, bool) {
	switch {
	case len(seg) == 3 && seg[0] == "workflow" && seg[1] == "parameters" && paramName.MatchString(seg[2]):
		if path, ok := ctxparam.Ref(seg[2]); ok {
			return expr.Ref{Path: path}, true
		}
		return expr.Ref{Path: []string{expr.PrefixInputs, seg[2]}}, true
	case len(seg) == 2 && seg[0] == loopItem && s == ScopeTask && paramName.MatchString(seg[1]):
		return expr.Ref{Path: []string{expr.PrefixMatrix, seg[1]}}, true
	case len(seg) == 3 && seg[0] == "inputs" && seg[1] == "parameters" && s == ScopeTemplate && paramName.MatchString(seg[2]):
		return expr.Ref{Path: []string{expr.PrefixMatrix, seg[2]}}, true
	case len(seg) == 5 && seg[0] == tasksHead && seg[2] == "outputs" && seg[3] == "parameters" && s == ScopeTask &&
		validTask(seg[1]) && paramName.MatchString(seg[4]):
		return expr.Ref{Path: []string{expr.PrefixNeeds, seg[1], "outputs", seg[4]}}, true
	}
	return expr.Ref{}, false
}

// RefExpr writes a canonical ref as an expr-lang member path with bracket indexing for every name, which a
// hyphenated name needs: workflow.parameters['ctx-branch']. env, secrets and vars have no form.
func RefExpr(r expr.Ref, s Scope) (string, bool) {
	seg, ok := segments(r, s)
	if !ok {
		return "", false
	}
	var b strings.Builder
	for i, p := range seg {
		switch {
		case p.name:
			b.WriteString("['" + p.text + "']")
		case i > 0:
			b.WriteString("." + p.text)
		default:
			b.WriteString(p.text)
		}
	}
	return b.String(), true
}

// RefTag writes a canonical ref as the simple tag that holds it: {{workflow.parameters.ctx-branch}}.
func RefTag(r expr.Ref, s Scope) (string, bool) {
	seg, ok := segments(r, s)
	if !ok || len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs && r.Path[1] == jsonParam {
		return "", false
	}
	texts := make([]string, len(seg))
	for i, p := range seg {
		texts[i] = p.text
	}
	return tagOpen + strings.Join(texts, ".") + tagClose, true
}

// FromTag inverts RefTag for text that is exactly one simple tag.
func FromTag(w string, s Scope) (expr.Ref, bool) {
	inner, ok := strings.CutPrefix(w, tagOpen)
	if !ok {
		return expr.Ref{}, false
	}
	inner, ok = strings.CutSuffix(inner, tagClose)
	if !ok {
		return expr.Ref{}, false
	}
	return fromTagBody(inner, s)
}

func fromTagBody(body string, s Scope) (expr.Ref, bool) {
	r, ok := fromSegments(strings.Split(strings.TrimSpace(body), "."), s)
	if !ok || len(r.Path) == 2 && r.Path[0] == expr.PrefixInputs && r.Path[1] == jsonParam {
		return expr.Ref{}, false
	}
	return r, true
}
