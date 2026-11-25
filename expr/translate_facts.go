package expr

import (
	"slices"
	"strings"
)

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
	eventPush   = "push"
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
//     and may be true in GHA, where ref_name is <number>/merge. A test of an unguarded ref_name that raises the
//     condition gains the pull request exclusion (AssumeRefName): GitLab holds the source branch there.
//   - GitLab to a target that holds the target branch (AssumeGitLab): each branch test gains the pull request
//     test.
//   - To GHA, and from a source that holds the target branch to GitLab: a branch test whose result can differ
//     on a pull request in the direction that runs more is refused. A GitLab-class ref_name test that
//     lowers the condition gains the exclusion for GHA (AssumeGitLabRef).
//   - A target with several pull request events (Dialect.PRFamily) states each pull request test as a prefix test.
func TranslateFacts(e Expr, from, to Dialect, f Facts) (Expr, bool) {
	for _, v := range exactVariants(e, from, to) {
		for _, x := range rewrites(v.e, v.from, to, f) {
			if t, ok := translate(x, v.from, to, listsFor(v.e)); ok {
				if to.PRFamily {
					t = prFamily(t, to, v.from.PRStates, false)
				}
				return v.finish(t), true
			}
		}
	}
	return nil, false
}

// TranslatableFacts reports whether TranslateFacts succeeds. It states the subset as the Lean model does:
// guardOk, and the condition after the rewrite is Translatable (lean4/Pipemaster/TranslateFacts.lean).
func TranslatableFacts(e Expr, from, to Dialect, f Facts) bool {
	return slices.ContainsFunc(exactVariants(e, from, to), func(v variant) bool {
		return slices.ContainsFunc(rewrites(v.e, v.from, to, f), func(x Expr) bool { return translatable(x, v.from, to, listsFor(v.e)) })
	})
}

// rewrites lists the conditions to try, best first. A GitLab-class source gets the pull request test in the
// idiom of the test it guards, and then as a pattern test, which a target that reads no event equality takes.
func rewrites(e Expr, from, to Dialect, f Facts) []Expr {
	x, ok := rewriteFacts(e, from, to, f)
	if !ok {
		return nil
	}
	out := []Expr{x}
	if route(from, to) == routeGLToBranch {
		if y, yok := assumeGitLab(e, from, true); yok {
			out = append(out, y)
		}
	}
	return out
}

// rewriteFacts is the condition to translate, and false when the pair has no safe form of it.
func rewriteFacts(e Expr, from, to Dialect, f Facts) (Expr, bool) {
	switch route(from, to) {
	case routeGHAFacts:
		if guardedAffix(e) || to.PRFamily && bareEvent(e) {
			return nil, false
		}
		return Assume(e, f.Branch), true
	case routeGHAToGL:
		if guardedAffixUnderNot(e, false) {
			return nil, false
		}
		return AssumeRefName(e, false)
	case routeGLToBranch:
		return AssumeGitLab(e, from)
	case routeToGHA:
		if !from.prPaired && riskyBranch(e, false, toGHARisk(from)) {
			return nil, false
		}
		return AssumeGitLabRef(e, from, false)
	case routeBranchToGL:
		return e, from.prPaired || !riskyBranch(e, false, branchToGLRisk)
	}
	return e, true
}

// bareEvent (Mirrors NoBareEv: lean4/Pipemaster/TranslateFacts.lean) finds the event ref used as a value under !, &&
// and ||: its string differs between the pull request events of a dialect that has several.
func bareEvent(e Expr) bool {
	switch v := e.(type) {
	case Ref:
		return pathName(v) == CtxEvent
	case Unary:
		return v.Op == OpNot && bareEvent(v.X)
	case Binary:
		return (v.Op == OpAnd || v.Op == OpOr) && (bareEvent(v.L) || bareEvent(v.R))
	}
	return false
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

// defaultBranchProbe stands for the default branch name in riskyBranch: a literal that no pull request ref <number>/merge equals.
const defaultBranchProbe = "default-branch"

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
			if defaultBranchCmp(v) && (v.Op == OpEq || v.Op == OpNe) {
				// A default branch is never named <number>/merge, so the test misses a GHA pull request ref like a literal that does.
				return risk((v.Op == OpEq) != odd, ModeExact, defaultBranchProbe)
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
func AssumeGitLab(e Expr, from Dialect) (Expr, bool) { return assumeGitLab(e, from, false) }

// assumeGitLab is AssumeGitLab; patterns says to state the pull request test as a pattern test in every case.
func assumeGitLab(e Expr, from Dialect, patterns bool) (Expr, bool) {
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			x, ok := assumeGitLab(v.X, from, patterns)
			return Unary{Op: OpNot, X: x}, ok
		}
	case Binary:
		switch v.Op {
		case OpAnd, OpOr:
			l, lok := assumeGitLab(v.L, from, patterns)
			r, rok := assumeGitLab(v.R, from, patterns)
			return Binary{Op: v.Op, L: l, R: r}, lok && rok
		case OpMatch, OpNoMatch, OpEq, OpNe:
			return assumeBranchLeaf(v, from, patterns)
		}
	}
	return e, noBranch(e)
}

func noBranch(e Expr) bool { return walkRefs(e, func(r Ref) bool { return pathName(r) != CtxBranch }) }

func assumeBranchLeaf(b Binary, from Dialect, patterns bool) (Expr, bool) {
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
	eqForm := !patterns && (b.Op == OpEq || b.Op == OpNe)
	// match builds the test of the pull request event in the idiom of the leaf, so that a target that reads only
	// that idiom (Drone) accepts it.
	match := func(positive bool) Expr {
		if eqForm {
			eq := Binary{Op: OpEq, L: ctxRef(CtxEvent), R: Lit{Kind: LitStr, Str: pr}}
			if positive {
				return eq
			}
			return Unary{Op: OpNot, X: eq}
		}
		op := OpNoMatch
		if positive {
			op = OpMatch
		}
		return Binary{Op: op, L: ctxRef(CtxEvent), R: Lit{Kind: LitStr, Str: Pattern(ModeExact, pr)}}
	}
	if b.Op == OpMatch || b.Op == OpEq {
		return Binary{Op: OpAnd, L: match(false), R: b}, true
	}
	return Binary{Op: OpOr, L: match(true), R: b}, true
}

// AssumeRefName (Mirrors ghaGlRn_guard_ok: lean4/Pipemaster/TranslateFacts.lean) guards the tests of an
// unguarded ref_name that go to GitLab. A GHA pull request has the ref_name <number>/merge, but a GitLab merge
// request pipeline holds the source branch name in CI_COMMIT_REF_NAME (lib/gitlab/ci/variables/builder/pipeline.rb:72
// and app/models/ci/pipeline.rb:1871 of gitlab-org/gitlab), which a literal can equal. A test that raises the
// condition (== under an even number of negations, != under an odd number) is false in GHA there, so it gains
// the pull request test. A test that lowers it cannot run GitLab more often and stays. An affix test whose
// literal a <number>/merge name can pass has no rewrite. A branch guard keeps its test: it becomes a test of the
// branch ref, which GitLab leaves unset there.
func AssumeRefName(e Expr, odd bool) (Expr, bool) {
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			x, ok := AssumeRefName(v.X, !odd)
			return Unary{Op: OpNot, X: x}, ok
		}
	case Call:
		if m := affixMode(v); m != nil && nameTest(v) {
			lit, _ := isStr(v.Args[1])
			if !prMissesAffix(*m, lit) {
				return nil, false
			}
			if odd {
				return v, true
			}
			return Binary{Op: OpAnd, L: eventTest(OpNe), R: v}, true
		}
	case Binary:
		switch v.Op {
		case OpEq, OpNe:
			if !isNameCmp(v) {
				return v, true
			}
			switch {
			case v.Op == OpEq && !odd:
				return Binary{Op: OpAnd, L: eventTest(OpNe), R: v}, true
			case v.Op == OpNe && odd:
				return Binary{Op: OpOr, L: eventTest(OpEq), R: v}, true
			}
			return v, true
		case OpAnd:
			if _, g := typeGuard(v.L); g && nameTest(v.R) {
				return v, true
			}
			fallthrough
		case OpOr:
			l, lok := AssumeRefName(v.L, odd)
			r, rok := AssumeRefName(v.R, odd)
			return Binary{Op: v.Op, L: l, R: r}, lok && rok
		}
	}
	return e, true
}

// AssumeGitLabRef (Mirrors glRn: lean4/Pipemaster/TranslateFacts.lean) guards the tests of CI_COMMIT_REF_NAME in a
// GitLab-class condition for GHA. In a merge request pipeline the variable holds the source branch name
// (lib/gitlab/ci/variables/builder/pipeline.rb:72 and app/models/ci/pipeline.rb:1871 of gitlab-org/gitlab), but
// the GHA ref_name on a pull request is <number>/merge. A test that lowers the condition is false there in GHA:
// a negative test under an even number of negations gains the pull request exclusion, and a positive test under
// an odd number gains the pull request alternative. A test that raises the condition has no rewrite when a
// <number>/merge name can pass it.
func AssumeGitLabRef(e Expr, from Dialect, odd bool) (Expr, bool) {
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			x, ok := AssumeGitLabRef(v.X, from, !odd)
			return Unary{Op: OpNot, X: x}, ok
		}
	case Binary:
		switch v.Op {
		case OpAnd, OpOr:
			l, lok := AssumeGitLabRef(v.L, from, odd)
			r, rok := AssumeGitLabRef(v.R, from, odd)
			return Binary{Op: v.Op, L: l, R: r}, lok && rok
		case OpMatch, OpNoMatch, OpEq, OpNe:
			m, lit, ok := nameLeaf(v)
			if !ok {
				return v, true
			}
			pr, has := from.Events[eventPR]
			if !has {
				return v, false
			}
			test := func(op string) Expr {
				return Binary{Op: op, L: ctxRef(CtxEvent), R: Lit{Kind: LitStr, Str: Pattern(ModeExact, pr)}}
			}
			pos := v.Op == OpMatch || v.Op == OpEq
			switch {
			case pos != odd:
				return v, prMissesAffix(m, lit)
			case pos:
				return Binary{Op: OpOr, L: test(OpMatch), R: v}, true
			}
			return Binary{Op: OpAnd, L: test(OpNoMatch), R: v}, true
		}
	}
	return e, true
}

// nameLeaf recognises a test of ref_name against a string, and its mode and literal.
func nameLeaf(b Binary) (Mode, string, bool) {
	switch b.Op {
	case OpMatch, OpNoMatch:
		ref, isRef := b.L.(Ref)
		pat, isPat := isStr(b.R)
		m, s, parsed := ParsePattern(pat)
		return m, s, isRef && isPat && parsed && pathName(ref) == CtxRefName
	case OpEq, OpNe:
		ref, lit, isCmp := refSide(b.L, b.R)
		return ModeExact, lit, isCmp && pathName(ref) == CtxRefName
	}
	return 0, "", false
}

// prFamily (Mirrors fam: lean4/Pipemaster/TranslateFacts.lean) writes each test of the pull request event as a
// prefix test, so that it holds for every pull request event of the dialect: pull_request_closed and
// pull_request_metadata as well as pull_request. A source whose pull request event has no such states (Drone: it
// never reports a closed pull request) keeps a test that a run must pass exact, since the prefix would admit events
// the source never has; posFam says the source has them. A test that a run must fail (under a negation, or the
// complement) takes the prefix whatever the source says: it excludes more. neg is true under an odd number of negations.
func prFamily(e Expr, to Dialect, posFam, neg bool) Expr {
	pr, ok := to.Events[eventPR]
	if !ok {
		return e
	}
	switch v := e.(type) {
	case Unary:
		if v.Op == OpNot {
			return Unary{Op: OpNot, X: prFamily(v.X, to, posFam, !neg)}
		}
	case Binary:
		switch v.Op {
		case OpAnd, OpOr:
			return Binary{Op: v.Op, L: prFamily(v.L, to, posFam, neg), R: prFamily(v.R, to, posFam, neg)}
		case OpEq, OpNe, OpMatch, OpNoMatch:
			isA := v.Op == OpEq || v.Op == OpMatch
			if t, ok := familyTest(v, pr); ok && (posFam || isA == neg) {
				return t
			}
		}
	}
	return e
}

// familyTest rewrites a test of the event ref against the literal pr.
func familyTest(b Binary, pr string) (Expr, bool) {
	ref, isRef := b.L.(Ref)
	if !isRef || pathName(ref) != CtxEvent {
		return nil, false
	}
	lit, isLit := isStr(b.R)
	if !isLit {
		return nil, false
	}
	op := OpMatch
	switch b.Op {
	case OpEq:
		if lit != pr {
			return nil, false
		}
	case OpNe:
		if lit != pr {
			return nil, false
		}
		op = OpNoMatch
	case OpMatch, OpNoMatch:
		if m, s, ok := ParsePattern(lit); !ok || m != ModeExact || s != pr {
			return nil, false
		}
		op = b.Op
	}
	return Binary{Op: op, L: copyRef(ref), R: Lit{Kind: LitStr, Str: Pattern(ModePrefix, pr)}}, true
}

// listsFor is true when e names the event: its tests are the source's own, so a negated one must not pass the
// events only the target has (eventListsRx). A condition with no event test gets only the pull request guards of
// rewriteFacts, which exclude one event on purpose.
func listsFor(e Expr) bool {
	found := false
	walkRefs(e, func(r Ref) bool { found = found || pathName(r) == CtxEvent; return !found })
	return found
}
