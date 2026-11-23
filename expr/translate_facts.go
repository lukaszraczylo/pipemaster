package expr

// Facts is what a pipeline's triggers guarantee about every run that can reach a condition (Mirrors expr.Facts
// (expr/translate_facts.go): lean4/Pipemaster/TranslateFacts.lean, BranchFact; a pull request has PrFact whatever
// the triggers).
type Facts struct {
	// Branch is true when no trigger can start a run on a tag ref. A run that is not a pull request then has a
	// branch ref, and a pull request run names its ref <number>/merge.
	Branch bool
}

const (
	eventPR     = "pull_request"
	refTypeName = "branch"
)

// usesFacts is true when the translation rewrites the condition: the source is in the GHA class and the target has
// no name for ref_name, so it can only state the branch ref. A target that names ref_name takes the plain path.
func usesFacts(from, to Dialect) bool {
	_, named := to.Vocab.FromCanon[CtxRefName]
	return from.Class == ClassGHA && !named
}

// TranslateFacts is Translate for a target that has no ref_name, where the branch ref is the only name for the
// ref: it holds the target branch on a pull request, and GHA ref_name there is <number>/merge. The condition is
// rewritten first (Assume): a branch-guarded ref_name equality excludes pull requests, and with the facts a
// ref_name comparison states them too. A branch-guarded affix test is refused, because no rewrite states its
// pull request result. Any other call is Translate.
func TranslateFacts(e Expr, from, to Dialect, f Facts) (Expr, bool) {
	if !usesFacts(from, to) {
		return Translate(e, from, to)
	}
	if guardedAffix(e) {
		return nil, false
	}
	return Translate(Assume(e, f.Branch), from, to)
}

// TranslatableFacts reports whether TranslateFacts succeeds. It states the subset as the Lean model does:
// guardOk, and the condition after Assume is Translatable (lean4/Pipemaster/TranslateFacts.lean, TranslatableWp).
func TranslatableFacts(e Expr, from, to Dialect, f Facts) bool {
	if !usesFacts(from, to) {
		return Translatable(e, from, to)
	}
	return !guardedAffix(e) && Translatable(Assume(e, f.Branch), from, to)
}

// guardedAffix (Mirrors guardOk: lean4/Pipemaster/TranslateFacts.lean) finds a branch guard on a startsWith, endsWith or contains test of ref_name. The branch ref
// stands for the name on a push, but on a pull request the GHA name is <number>/merge and the target holds the
// target branch, so the two tests differ there and no rewrite states the difference.
func guardedAffix(e Expr) bool {
	switch v := e.(type) {
	case Unary:
		return guardedAffix(v.X)
	case Binary:
		if v.Op == OpAnd {
			if g, ok := typeGuard(v.L); ok && g == CtxBranch && nameTest(v.R) && !isEqNameTest(v.R) {
				return true
			}
		}
		return guardedAffix(v.L) || guardedAffix(v.R)
	}
	return false
}

func isEqNameTest(e Expr) bool {
	b, ok := e.(Binary)
	return ok && b.Op == OpEq
}

// Assume rewrites a condition so that its pull request result does not depend on what the target holds in the
// branch ref (Mirrors expr.Assume (expr/translate_facts.go): lean4/Pipemaster/TranslateFacts.lean, assume,
// eval_assume). GHA ref_name on a pull request is <number>/merge, which no literal that okEq accepts equals. A
// branch guard on a ref_name == test stays and gains the pull request test. With branch, which says that no
// run has a tag ref, a bare ref_name comparison also gains it: == is false and != true on a pull request, and off
// one the ref is a branch. Any other guarded ref_name test stays as it is.
func Assume(e Expr, branch bool) Expr {
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			return Unary{Op: OpNot, X: Assume(v.X, branch)}
		}
	case Binary:
		return assumeBinary(v, branch)
	}
	return e
}

func assumeBinary(b Binary, branch bool) Expr {
	switch b.Op {
	case OpEq, OpNe:
		if !branch || !isNameCmp(b) {
			return b
		}
		eq := Binary{Op: OpEq, L: b.L, R: b.R}
		onBranch := Binary{Op: OpAnd, L: branchGuard(), R: eq}
		if b.Op == OpEq {
			return Binary{Op: OpAnd, L: eventTest(OpNe), R: onBranch}
		}
		return Binary{Op: OpOr, L: eventTest(OpEq), R: Unary{Op: OpNot, X: onBranch}}
	case OpAnd:
		if g, ok := typeGuard(b.L); ok && g == CtxBranch && isEqNameTest(b.R) && nameTest(b.R) {
			return Binary{Op: OpAnd, L: eventTest(OpNe), R: b}
		}
		if _, g := typeGuard(b.L); g && nameTest(b.R) {
			return b
		}
		return Binary{Op: OpAnd, L: Assume(b.L, branch), R: Assume(b.R, branch)}
	case OpOr:
		return Binary{Op: OpOr, L: Assume(b.L, branch), R: Assume(b.R, branch)}
	}
	return b
}

// isNameCmp: ref_name == or != a literal that no pull request ref_name can equal.
func isNameCmp(b Binary) bool {
	ref, lit, ok := refSide(b.L, b.R)
	return ok && pathName(ref) == CtxRefName && okEq(lit)
}

func eventTest(op string) Expr {
	return Binary{Op: op, L: ctxRef(CtxEvent), R: Lit{Kind: LitStr, Str: eventPR}}
}

func branchGuard() Expr {
	return Binary{Op: OpEq, L: ctxRef(CtxRefType), R: Lit{Kind: LitStr, Str: refTypeName}}
}
