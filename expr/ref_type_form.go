package expr

// RefTypeForm rewrites each test that the ref starts with refs/tags/ into a test of the ref type. The two hold on the
// same runs: a tag run, and no other. A target that reads the ref type but no function of the ref (Tekton, Argo) takes
// the second form. The prefix refs/heads/ has no such twin, because a pull request run has the ref type branch.
// The Lean model of Translate does not state this rewrite, so only the emitters that call it use it.
func RefTypeForm(e Expr) Expr {
	out, _ := rebuild(e, func(x Expr) (Expr, bool) {
		c, ok := x.(Call)
		if !ok || c.Name != fnStartsWith || len(c.Args) != 2 || !isRef(c.Args[0], CtxRef) {
			return nil, false
		}
		if lit, ok := isStr(c.Args[1]); !ok || lit != tagRefPrefix {
			return nil, false
		}
		return Binary{Op: OpEq, L: ctxRef(CtxRefType), R: strLit(refTypeTag)}, true
	})
	return out
}
