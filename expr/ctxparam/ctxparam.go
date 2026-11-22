// Package ctxparam names the reserved workflow parameter that carries each canonical context value, for the
// providers that have no event context of their own (Tekton, Argo).
package ctxparam

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Prefix starts every reserved context parameter name.
const Prefix = "ctx-"

// names are the canonical context names that a reserved param can carry, in declaration order.
var names = []string{
	expr.CtxBranch, expr.CtxTag, expr.CtxEvent, expr.CtxSHA, expr.CtxRepo, expr.CtxActor, expr.CtxPRNumber,
	expr.CtxRefName, expr.CtxRefType, expr.CtxRef, expr.CtxPRHeadRef, expr.CtxPRBaseRef,
}

var namer = strings.NewReplacer(".", "-", "_", "-")

// Names lists the canonical context names that a reserved param can carry. The caller owns the copy.
func Names() []string { return slices.Clone(names) }

// Param gives the reserved param that carries a canonical context ref: branch is ctx-branch, pr.number is
// ctx-pr-number and ref_name is ctx-ref-name.
func Param(path []string) (string, bool) {
	name := strings.Join(path, ".")
	if !slices.Contains(names, name) {
		return "", false
	}
	return Prefix + namer.Replace(name), true
}

// Ref inverts Param.
func Ref(param string) ([]string, bool) {
	for _, c := range names {
		if Prefix+namer.Replace(c) == param {
			return strings.Split(c, "."), true
		}
	}
	return nil, false
}
