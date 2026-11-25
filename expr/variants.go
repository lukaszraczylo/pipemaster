package expr

import (
	"maps"
	"slices"
)

// A variant is a rewrite of a condition that the exact translator refuses or states too narrowly. Each public
// entry tries the condition as written first, so a condition that translated before translates the same way; a
// variant only adds a result where there was none. No variant is covered by the Lean model: tests enumerate
// the contexts of each one against the source evaluation instead (variants_test.go).
type variant struct {
	e   Expr
	fix func(Expr) Expr
	// caveat is set when the variant holds on a run the source does not hold on, so only the approximate entry takes it.
	caveat string
	from   Dialect
}

// tagCaveat names the run that a tag name test adds: a manual or API run on a tag ref also sets the tag name, while
// the source event there is not the tag event.
const tagCaveat = "a manual or API run on a tag ref also matches; the source tag event holds on a tag push only"

// exactVariants lists the variants that hold on exactly the runs of the source.
func exactVariants(e Expr, from, to Dialect) []variant {
	return slices.DeleteFunc(variants(e, from, to), func(v variant) bool { return v.caveat != "" })
}

func (v variant) finish(t Expr) Expr {
	if v.fix == nil {
		return t
	}
	return v.fix(t)
}

func variants(e Expr, from, to Dialect) []variant {
	bases := []variant{{e: e, from: from}}
	for _, form := range []tagForm{tagMatch, tagBare} {
		if x, ok := tagVariant(e, from, form); ok {
			proxy, _ := tagVariant(e, from, tagProxy)
			v := variant{e: x, from: readsAs(from, proxy)}
			if !to.TagEvent {
				v.caveat = tagCaveat
			}
			bases = append(bases, v)
		}
	}
	if x, ok := refVariant(e); ok {
		bases = append(bases, variant{e: x, from: readsAs(from, e), fix: anyTagFix})
	}
	// A pair of tests is tried first: the test as written is false on a pull request where the source holds.
	var out []variant
	for _, b := range bases {
		if x, f, ok := prBranchVariant(b.e, b.from, to); ok {
			out = append(out, variant{e: x, from: f, fix: b.fix})
		}
	}
	return append(out, bases...)
}

func strLit(s string) Lit { return Lit{Kind: LitStr, Str: s} }

func rebuild(e Expr, leaf func(Expr) (Expr, bool)) (Expr, bool) {
	if x, ok := leaf(e); ok {
		return x, true
	}
	switch v := e.(type) {
	case Unary:
		x, ok := rebuild(v.X, leaf)
		return Unary{Op: v.Op, X: x}, ok
	case Binary:
		l, lok := rebuild(v.L, leaf)
		r, rok := rebuild(v.R, leaf)
		return Binary{Op: v.Op, L: l, R: r}, lok || rok
	case Call:
		args := make([]Expr, len(v.Args))
		changed := false
		for i, a := range v.Args {
			var ok bool
			args[i], ok = rebuild(a, leaf)
			changed = changed || ok
		}
		return Call{Name: v.Name, Args: args}, changed
	}
	return e, false
}

// tagForm is how a variant spells that the tag ref is set: a match of any non-empty value, which is a boolean
// and reads in every dialect, or the bare ref, for a target that reads no such pattern.
type tagForm uint8

const (
	tagMatch tagForm = iota
	tagBare
	// tagProxy puts a test of the branch in place of the tag test, so a dialect can judge the rest of the
	// condition by its own subset: both are a test of a ref against a literal.
	tagProxy
)

// tagVariant states an event test of tag as the tag ref being set: Woodpecker and Drone report a tag push as the
// tag event, and no other dialect has that event, while every dialect has a tag name that is set on exactly that run.
func tagVariant(e Expr, from Dialect, form tagForm) (Expr, bool) {
	if !from.TagEvent {
		return nil, false
	}
	return rebuild(e, func(x Expr) (Expr, bool) {
		b, ok := x.(Binary)
		if !ok {
			return nil, false
		}
		neg := false
		switch b.Op {
		case OpNe, OpNoMatch:
			neg = true
		case OpEq, OpMatch:
		default:
			return nil, false
		}
		switch b.Op {
		case OpEq, OpNe:
			r, s, ok := refSide(b.L, b.R)
			if !ok || pathName(r) != CtxEvent || s != "tag" {
				return nil, false
			}
		default:
			pat, pok := isStr(b.R)
			m, s, parsed := ParsePattern(pat)
			if !isRef(b.L, CtxEvent) || !pok || !parsed || m != ModeExact || s != "tag" {
				return nil, false
			}
		}
		set := Ref{Path: []string{CtxTag}}
		if form == tagProxy {
			return negE(neg, Binary{Op: OpEq, L: ctxRef(CtxBranch), R: strLit("x")}), true
		}
		if form == tagMatch {
			op := OpMatch
			if neg {
				op = OpNoMatch
			}
			return Binary{Op: op, L: set, R: Lit{Kind: LitStr, Str: anyValue}}, true
		}
		if neg {
			return Unary{Op: OpNot, X: set}, true
		}
		return set, true
	})
}

// StatesFullRef reports a condition that compares the full ref with a refs/heads/ or refs/tags/ literal. Only a GHA-class
// source can write that test, so a project of another origin holds it when a marker restored it from a GHA file.
func StatesFullRef(e Expr) bool {
	_, ok := refVariant(e)
	return ok
}

// refVariant states a full-ref test of a condition as the ref type and name tests, for a target that cannot
// state the full ref. github.ref is refs/heads/<name> or refs/tags/<name> on a push, so the test is the
// ref_type test with the name; on a pull request the ref is refs/pull/<n>/merge and the ref_type test is false
// for a tag and for any name that is not <n>/merge.
func refVariant(e Expr) (Expr, bool) {
	const heads, tags = "refs/heads/", "refs/tags/"
	return rebuild(e, func(x Expr) (Expr, bool) {
		switch v := x.(type) {
		case Binary:
			if v.Op != OpEq && v.Op != OpNe {
				return nil, false
			}
			r, lit, ok := refSide(v.L, v.R)
			if !ok || pathName(r) != CtxRef {
				return nil, false
			}
			var typ, name string
			switch {
			case len(lit) > len(heads) && lit[:len(heads)] == heads:
				typ, name = CtxBranch, lit[len(heads):]
			case len(lit) > len(tags) && lit[:len(tags)] == tags:
				typ, name = CtxTag, lit[len(tags):]
			default:
				return nil, false
			}
			if !okEq(name) {
				return nil, false
			}
			return negE(v.Op == OpNe, guardE(typ, Binary{Op: OpEq, L: ctxRef(CtxRefName), R: strLit(name)})), true
		case Call:
			m := affixMode(v)
			if m == nil || *m != ModePrefix {
				return nil, false
			}
			r, rok := v.Args[0].(Ref)
			lit, lok := isStr(v.Args[1])
			if !rok || !lok || pathName(r) != CtxRef {
				return nil, false
			}
			switch {
			case lit == tags:
				return guardE(CtxTag, Binary{Op: OpEq, L: ctxRef(CtxRefName), R: strLit(anySentinel)}), true
			case len(lit) > len(tags) && lit[:len(tags)] == tags && okLit(lit[len(tags):]):
				return guardE(CtxTag, predE(ModePrefix, ctxRef(CtxRefName), lit[len(tags):])), true
			case len(lit) > len(heads) && lit[:len(heads)] == heads && okLit(lit[len(heads):]):
				return guardE(CtxBranch, predE(ModePrefix, ctxRef(CtxRefName), lit[len(heads):])), true
			}
		}
		return nil, false
	})
}

// prBranchVariant pairs each test of the branch ref with the same test of the pull request target branch. A
// Woodpecker or Drone pull request run holds the target branch in the branch ref, which GHA and GitLab leave
// as <n>/merge or unset there, and both hold the target branch in their own base ref. On a run that is no pull
// request the base ref is unset, so the pair equals the source test on every run: a positive test is the
// disjunction and a negative one the conjunction. A branch ref used in any other shape gets no variant.
func prBranchVariant(e Expr, from, to Dialect) (Expr, Dialect, bool) {
	if !from.BranchOnPR || to.BranchOnPR {
		return nil, from, false
	}
	other := false
	leaves := 0
	x, ok := rebuild(e, func(x Expr) (Expr, bool) {
		b, bok := x.(Binary)
		if !bok {
			return nil, false
		}
		var rhs Expr
		switch l, lok := b.L.(Ref); {
		case lok && pathName(l) == CtxBranch:
			rhs = b.R
		default:
			r, rok := b.R.(Ref)
			if !rok || pathName(r) != CtxBranch {
				return nil, false
			}
			rhs = b.L
		}
		if _, isLit := rhs.(Lit); !isLit {
			other = true
			return nil, false
		}
		var swapped Binary
		if isRef(b.L, CtxBranch) {
			swapped = Binary{Op: b.Op, L: ctxRef(CtxPRBaseRef), R: rhs}
		} else {
			swapped = Binary{Op: b.Op, L: rhs, R: ctxRef(CtxPRBaseRef)}
		}
		leaves++
		switch b.Op {
		case OpEq, OpMatch:
			return Binary{Op: OpOr, L: b, R: swapped}, true
		case OpNe, OpNoMatch:
			return Binary{Op: OpAnd, L: b, R: swapped}, true
		}
		other = true
		return nil, false
	})
	if !ok || other {
		return nil, from, false
	}
	if leaves == 0 || leaves != countBranch(e) {
		return nil, from, false
	}
	f := from
	f.prPaired = true
	if _, has := f.Vocab.FromCanon[CtxPRBaseRef]; !has {
		f.Vocab = Vocab{ToCanon: maps.Clone(from.Vocab.ToCanon), FromCanon: maps.Clone(from.Vocab.FromCanon)}
		f.Vocab.ToCanon[droneTarget] = []string{"pr", "base_ref"}
		f.Vocab.FromCanon[CtxPRBaseRef] = []string{droneTarget}
	}
	return x, readsAs(f, e), true
}

// droneTarget is the Drone variable of the pull request target branch; a push run reports the branch there.
const droneTarget = "DRONE_TARGET_BRANCH"

func countBranch(e Expr) int {
	n := 0
	walkRefs(e, func(r Ref) bool {
		if pathName(r) == CtxBranch {
			n++
		}
		return true
	})
	return n
}

// readsAs makes d accept a variant when it accepts the condition the variant was made from: the variant is
// the same test in another spelling, which the dialect's own subset need not hold.
func readsAs(d Dialect, orig Expr) Dialect {
	if d.Reads != nil {
		read := d.Reads
		d.Reads = func(Expr) bool { return read(orig) }
	}
	return d
}

// anySentinel stands in a variant for "any name": the translator takes a ref_type test only with a name test, so
// the test of a bare tag ref carries this name and anyTagFix turns the name test into the any-value pattern.
const anySentinel = "pipemaster-any-name"

func anyTagFix(t Expr) Expr {
	r, _ := rebuild(t, func(x Expr) (Expr, bool) {
		b, ok := x.(Binary)
		if !ok || (b.Op != OpMatch && b.Op != OpNoMatch) {
			return nil, false
		}
		pat, pok := isStr(b.R)
		if m, s, parsed := ParsePattern(pat); !pok || !parsed || m != ModeExact || s != anySentinel {
			return nil, false
		}
		return Binary{Op: b.Op, L: b.L, R: strLit(anyValue)}, true
	})
	return r
}
