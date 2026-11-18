package irtest

import (
	"math"

	"github.com/lukaszraczylo/pipemaster/expr"
	"pgregory.net/rapid"
)

var (
	ctxNames  = []string{expr.CtxBranch, expr.CtxTag, expr.CtxEvent, expr.CtxSHA, expr.CtxRepo, expr.CtxActor}
	prefixes  = []string{expr.PrefixMatrix, expr.PrefixEnv, expr.PrefixSecrets, expr.PrefixVars, expr.PrefixInputs, expr.PrefixNeeds}
	funcNames = []string{"contains", "startsWith", "endsWith", "format", "join", "toJSON", "fromJSON", "success", "failure", "cancelled", "always"}
	binOps    = []string{expr.OpEq, expr.OpNe, expr.OpLt, expr.OpLe, expr.OpGt, expr.OpGe, expr.OpAnd, expr.OpOr, expr.OpMatch, expr.OpNoMatch}
	numbers   = []float64{0, math.Copysign(0, -1), 1, -1, 0.1, 2.5, 1e21, 1e-7, 123456789, math.MaxFloat64, math.SmallestNonzeroFloat64}
)

const (
	segText = `[A-Za-z_][A-Za-z0-9_-]{0,6}`
	// strText includes quotes, spaces and brackets to stress escaping.
	strText = `[ -~]{0,8}`
)

// GenExpr draws an expression over the canonical vocabulary, at most maxDepth operator levels deep.
// Opaque nodes appear (at any level) only when opaque is true. Unused Lit fields stay zero and empty slices are nil,
// so a printed-and-parsed copy compares equal under go-cmp.
func GenExpr(t *rapid.T, label string, maxDepth int, opaque bool) expr.Expr {
	return genExpr(t, label, maxDepth, opaque, funcNames)
}

// GenExprAdversarial is GenExpr plus calls named like the reserved Opaque form, which expr.Check must reject.
func GenExprAdversarial(t *rapid.T, label string, maxDepth int, opaque bool) expr.Expr {
	return genExpr(t, label, maxDepth, opaque, append([]string{"opaque"}, funcNames...))
}

func genExpr(t *rapid.T, label string, maxDepth int, opaque bool, names []string) expr.Expr {
	kinds := 3
	if maxDepth > 0 {
		kinds = 6
	}
	if opaque {
		kinds++
	}
	k := rapid.IntRange(0, kinds-1).Draw(t, label+"k")
	switch {
	case k == 0:
		return genLit(t, label)
	case k == 1 || k == 2:
		return genRef(t, label)
	case maxDepth <= 0:
		return expr.Opaque{Dialect: rapid.SampledFrom([]string{"gha", "gitlab", "a)b"}).Draw(t, label+"d"), Text: rapid.StringMatching(strText).Draw(t, label+"t")}
	case k == 3:
		return expr.Unary{Op: expr.OpNot, X: genExpr(t, label+"u", maxDepth-1, opaque, names)}
	case k == 4:
		return expr.Binary{
			Op: rapid.SampledFrom(binOps).Draw(t, label+"o"),
			L:  genExpr(t, label+"l", maxDepth-1, opaque, names),
			R:  genExpr(t, label+"r", maxDepth-1, opaque, names),
		}
	case k == 5:
		c := expr.Call{Name: rapid.SampledFrom(names).Draw(t, label+"f")}
		for i, n := 0, rapid.IntRange(0, 2).Draw(t, label+"n"); i < n; i++ {
			c.Args = append(c.Args, genExpr(t, label+"a"+string(rune('0'+i)), maxDepth-1, opaque, names))
		}
		return c
	}
	return expr.Opaque{Dialect: rapid.SampledFrom([]string{"gha", "gitlab", "a)b"}).Draw(t, label+"d"), Text: rapid.StringMatching(strText).Draw(t, label+"t")}
}

func genLit(t *rapid.T, label string) expr.Expr {
	switch rapid.IntRange(0, 3).Draw(t, label+"lk") {
	case 0:
		return expr.Lit{}
	case 1:
		return expr.Lit{Kind: expr.LitBool, Bool: rapid.Bool().Draw(t, label+"b")}
	case 2:
		return expr.Lit{Kind: expr.LitNum, Num: rapid.OneOf(rapid.SampledFrom(numbers), rapid.Float64Range(-1e12, 1e12)).Draw(t, label+"num")}
	}
	return expr.Lit{Kind: expr.LitStr, Str: rapid.StringMatching(strText).Draw(t, label+"s")}
}

func genRef(t *rapid.T, label string) expr.Expr {
	seg := func(n string) string {
		if rapid.IntRange(0, 5).Draw(t, label+n+"odd") == 0 {
			return rapid.StringMatching(`[ -~]{1,6}`).Draw(t, label+n+"oddv")
		}
		return rapid.StringMatching(segText).Draw(t, label+n)
	}
	switch rapid.IntRange(0, 3).Draw(t, label+"rk") {
	case 0:
		return expr.Ref{Path: []string{rapid.SampledFrom(ctxNames).Draw(t, label+"c")}}
	case 1:
		return expr.Ref{Path: []string{"pr", "number"}}
	case 2:
		return expr.Ref{Path: []string{rapid.SampledFrom(prefixes).Draw(t, label+"p"), seg("t0")}}
	}
	return expr.Ref{Path: []string{expr.PrefixNeeds, seg("j"), "outputs", seg("o")}}
}
