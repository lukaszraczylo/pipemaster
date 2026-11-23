package expr

import "strings"

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

// prRoute is how a pair of dialects treats the branch ref on a pull request run.
type prRoute uint8

const (
	routeNone       prRoute = iota // Translate states the pair exactly
	routeGHAFacts                  // GHA to a target with no ref_name: Assume (round 1)
	routeGHAToGL                   // GHA to GitLab: a branch-guarded affix under ! is refused
	routeGLToBranch                // GitLab to a target that holds the target branch: AssumeGitLab
	routeToGHA                     // a GitLab-class source to GHA: risky branch tests are refused
	routeBranchToGL                // a source that holds the target branch to GitLab: ! over a branch test is refused
)

// route picks the rule for a pair. The branch ref is unset in a GitLab merge request pipeline, holds the target
// branch on a Woodpecker or Drone pull request run, and GHA ref_name there is <number>/merge.
func route(from, to Dialect) prRoute {
	_, named := to.Vocab.FromCanon[CtxRefName]
	switch {
	case from.Class == ClassGHA && !named:
		return routeGHAFacts
	case from.Class == ClassGHA && to.Class == ClassGitLab && !to.BranchOnPR:
		return routeGHAToGL
	case from.Class == ClassGitLab && to.Class == ClassGHA:
		return routeToGHA
	case from.Class == ClassGitLab && to.Class == ClassGitLab && !from.BranchOnPR && to.BranchOnPR:
		return routeGLToBranch
	case from.Class == ClassGitLab && to.Class == ClassGitLab && from.BranchOnPR && !to.BranchOnPR:
		return routeBranchToGL
	}
	return routeNone
}

// TranslateFacts is Translate where the branch ref differs between the dialects on a pull request run
// (lean4/Pipemaster/TranslateFacts.lean). It rewrites or refuses what Translate would state wrongly there, and
// is Translate for every other pair. It never states a condition that holds on a run where the source does not.
//   - GHA to a target with no ref_name (Assume): a branch-guarded ref_name equality excludes pull requests, and
//     with the facts a ref_name comparison states them too. A branch-guarded affix test is refused.
//   - GHA to GitLab: a branch-guarded affix test under ! is refused: it is false in GitLab on a merge request
//     and may be true in GHA, where ref_name is <number>/merge.
//   - GitLab to a target that holds the target branch (AssumeGitLab): each branch test gains the pull request
//     test.
//   - To GHA, and from a source that holds the target branch to GitLab: a branch test whose result can differ
//     on a pull request in the direction that runs more is refused.
func TranslateFacts(e Expr, from, to Dialect, f Facts) (Expr, bool) {
	x, ok := rewriteFacts(e, from, to, f)
	if !ok {
		return nil, false
	}
	return Translate(x, from, to)
}

// TranslatableFacts reports whether TranslateFacts succeeds. It states the subset as the Lean model does:
// guardOk, and the condition after the rewrite is Translatable (lean4/Pipemaster/TranslateFacts.lean).
func TranslatableFacts(e Expr, from, to Dialect, f Facts) bool {
	x, ok := rewriteFacts(e, from, to, f)
	return ok && Translatable(x, from, to)
}

// rewriteFacts is the condition to translate, and false when the pair has no safe form of it.
func rewriteFacts(e Expr, from, to Dialect, f Facts) (Expr, bool) {
	switch route(from, to) {
	case routeGHAFacts:
		if guardedAffix(e) {
			return nil, false
		}
		return Assume(e, f.Branch), true
	case routeGHAToGL:
		return e, !guardedAffixUnderNot(e, false)
	case routeGLToBranch:
		return AssumeGitLab(e, from)
	case routeToGHA:
		return e, !riskyBranch(e, false, toGHARisk(from))
	case routeBranchToGL:
		return e, !riskyBranch(e, false, branchToGLRisk)
	}
	return e, true
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

// guardedAffixUnderNot finds a branch-guarded affix test (see guardedAffix) under an odd number of negations whose
// literal a pull request ref_name <number>/merge can match.
func guardedAffixUnderNot(e Expr, odd bool) bool {
	switch v := e.(type) {
	case Unary:
		return guardedAffixUnderNot(v.X, !odd)
	case Binary:
		if v.Op == OpAnd {
			if g, ok := typeGuard(v.L); ok && g == CtxBranch && nameTest(v.R) && !isEqNameTest(v.R) {
				c := v.R.(Call)
				lit, _ := isStr(c.Args[1])
				return odd && !prMissesAffix(*affixMode(c), lit)
			}
		}
		return guardedAffixUnderNot(v.L, odd) || guardedAffixUnderNot(v.R, odd)
	}
	return false
}

// prMissesAffix is true when no GHA pull request ref_name, <number>/merge, passes the test: a prefix that does
// not start with a digit, or a suffix or substring with a character outside those the name is made of. An exact
// test is always one, because okEq excludes a literal that starts with a digit.
func prMissesAffix(m Mode, lit string) bool {
	switch m {
	case ModeExact:
		return true
	case ModePrefix:
		return lit != "" && (lit[0] < '0' || lit[0] > '9')
	}
	return strings.ContainsFunc(asciiLower(lit), func(r rune) bool { return !strings.ContainsRune(prNameChars, r) })
}

// prNameChars are the characters of "<number>/merge" in lower case.
const prNameChars = "0123456789/merge"

// toGHARisk is the branch tests that GHA runs more often than the source on a pull request. A GitLab merge
// request leaves the branch unset, so a positive test is false there, and GHA is no higher when no
// <number>/merge ref_name passes the test (prMissesAffix). A source that holds the target branch can hold any
// value there, so a test whose result falls with the branch is risky as well.
func toGHARisk(from Dialect) func(effPos bool, m Mode, lit string) bool {
	return func(effPos bool, m Mode, lit string) bool {
		return effPos && !prMissesAffix(m, lit) || !effPos && from.BranchOnPR
	}
}

// branchToGLRisk: the target has no branch on a pull request, so a test under ! is true there where the source
// may hold the target branch and not.
func branchToGLRisk(effPos bool, _ Mode, _ string) bool { return !effPos }

// riskyBranch walks e and reports a branch test for which risk(effPos, mode, literal) is true. effPos is true
// when the condition rises with the test: a match or equality under an even number of negations, or a negated
// one under an odd number. Any other use of the branch ref is risky.
func riskyBranch(e Expr, odd bool, risk func(effPos bool, m Mode, lit string) bool) bool {
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			return riskyBranch(v.X, !odd, risk)
		}
	case Binary:
		switch v.Op {
		case OpAnd, OpOr:
			return riskyBranch(v.L, odd, risk) || riskyBranch(v.R, odd, risk)
		case OpMatch, OpNoMatch, OpEq, OpNe:
			if m, lit, ok := branchLeaf(v); ok {
				return risk((v.Op == OpMatch || v.Op == OpEq) != odd, m, lit)
			}
		}
	}
	return !noBranch(e)
}

// branchLeaf recognises a test of the branch ref against a string, and its mode and literal.
func branchLeaf(b Binary) (Mode, string, bool) {
	switch b.Op {
	case OpMatch, OpNoMatch:
		ref, isRef := b.L.(Ref)
		pat, isPat := isStr(b.R)
		m, s, parsed := ParsePattern(pat)
		return m, s, isRef && isPat && parsed && s != "" && pathName(ref) == CtxBranch
	case OpEq, OpNe:
		ref, lit, isCmp := refSide(b.L, b.R)
		return ModeExact, lit, isCmp && pathName(ref) == CtxBranch
	}
	return 0, "", false
}

// AssumeGitLab (Mirrors glPr: lean4/Pipemaster/TranslateFacts.lean) adds the pull request test to every test of
// the branch ref in a GitLab-class condition, for a target that holds the target branch on a pull request. The
// source leaves the branch unset in a merge request pipeline, so a positive test is false there and a negative
// one is true. A use of the branch ref in any other shape has no rewrite.
func AssumeGitLab(e Expr, from Dialect) (Expr, bool) {
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			x, ok := AssumeGitLab(v.X, from)
			return Unary{Op: OpNot, X: x}, ok
		}
	case Binary:
		switch v.Op {
		case OpAnd, OpOr:
			l, lok := AssumeGitLab(v.L, from)
			r, rok := AssumeGitLab(v.R, from)
			return Binary{Op: v.Op, L: l, R: r}, lok && rok
		case OpMatch, OpNoMatch, OpEq, OpNe:
			return assumeBranchLeaf(v, from)
		}
	}
	return e, noBranch(e)
}

func noBranch(e Expr) bool { return walkRefs(e, func(r Ref) bool { return pathName(r) != CtxBranch }) }

func assumeBranchLeaf(b Binary, from Dialect) (Expr, bool) {
	if noBranch(b) {
		return b, true
	}
	if _, _, ok := branchLeaf(b); !ok {
		return b, false
	}
	if ref, isRef := b.L.(Ref); !isRef || pathName(ref) != CtxBranch {
		return b, false
	}
	if lit, isLit := isStr(b.R); (b.Op == OpEq || b.Op == OpNe) && (!isLit || lit == "") {
		return b, false
	}
	pr, ok := from.Events[eventPR]
	if !ok {
		return b, false
	}
	test := func(op string) Expr {
		return Binary{Op: op, L: ctxRef(CtxEvent), R: Lit{Kind: LitStr, Str: Pattern(ModeExact, pr)}}
	}
	if b.Op == OpMatch || b.Op == OpEq {
		return Binary{Op: OpAnd, L: test(OpNoMatch), R: b}, true
	}
	return Binary{Op: OpOr, L: test(OpMatch), R: b}, true
}
