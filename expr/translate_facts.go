package expr

// Facts is what a pipeline's triggers guarantee about every run that can reach a condition (Mirrors expr.Facts
// (expr/translate_facts.go): lean4/Pipemaster/TranslateFacts.lean, BranchFact).
type Facts struct {
	// Branch is true when no trigger can start a run on a tag ref. A run that is not a pull request then has a
	// branch ref, and a pull request run names its ref <number>/merge.
	Branch bool
}

const (
	eventPR     = "pull_request"
	refTypeName = "branch"
)

// usesFacts is true when the facts change the translation: the source is in the GHA class and the target has no
// name for ref_name, so it can only state the branch ref. A target that names ref_name takes the plain path.
func (f Facts) usesFacts(from, to Dialect) bool {
	_, named := to.Vocab.FromCanon[CtxRefName]
	return f.Branch && from.Class == ClassGHA && !named
}

// TranslateFacts is Translate with the facts of the pipeline. The facts apply to a GHA-class source and a target
// with no name for ref_name; the condition is then translated with the facts stated in it, which lets a ref_name
// comparison reach the branch ref. Any other call is Translate. A target that reports the target branch on a pull
// request makes a branch-guarded affix test unsafe (a pull request ref_name <number>/merge can match an affix),
// so that test is refused.
func TranslateFacts(e Expr, from, to Dialect, f Facts) (Expr, bool) {
	if !f.usesFacts(from, to) {
		return Translate(e, from, to)
	}
	if guardedAffix(e) {
		return nil, false
	}
	return Translate(AssumeBranch(e), from, to)
}

// TranslatableFacts reports whether TranslateFacts succeeds. It states the subset as the Lean model does:
// the condition after AssumeBranch is Translatable.
func TranslatableFacts(e Expr, from, to Dialect, f Facts) bool {
	if !f.usesFacts(from, to) {
		return Translatable(e, from, to)
	}
	return !guardedAffix(e) && Translatable(AssumeBranch(e), from, to)
}

// guardedAffix finds a branch guard on a startsWith, endsWith or contains test of ref_name. The branch ref
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

// AssumeBranch states the branch facts in each ref_name comparison with a literal the translator can state
// (Mirrors expr.AssumeBranch (expr/translate_facts.go): lean4/Pipemaster/TranslateFacts.lean, assumeBranch,
// eval_assume). On a pull request ref_name is <number>/merge, which no such literal equals, so == is false there
// and != is true. Off a pull request the ref is a branch. A branch guard on a == test stays and gains the pull
// request test; any other guarded ref_name test stays as it is.
func AssumeBranch(e Expr) Expr {
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			return Unary{Op: OpNot, X: AssumeBranch(v.X)}
		}
	case Binary:
		return assumeBinary(v)
	}
	return e
}

func assumeBinary(b Binary) Expr {
	switch b.Op {
	case OpEq, OpNe:
		if !isNameCmp(b) {
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
		return Binary{Op: OpAnd, L: AssumeBranch(b.L), R: AssumeBranch(b.R)}
	case OpOr:
		return Binary{Op: OpOr, L: AssumeBranch(b.L), R: AssumeBranch(b.R)}
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
