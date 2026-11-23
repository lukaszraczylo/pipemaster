package expr

import (
	"regexp"
	"sort"
	"strings"
)

// Dialect describes one expression language for translation.
type Dialect struct {
	// Reads, when set, restricts the expressions Translate takes from this dialect to the subset that shares
	// the class semantics; the rest of the dialect's language evaluates differently (Woodpecker, where an
	// unset name reads "" and not null: lean4/Pipemaster/Woodpecker.lean).
	Reads func(Expr) bool
	// Supports, when set, accepts the expressions the dialect can print and that stay inside its subset.
	Supports func(Expr) bool
	// Native, when set, rewrites an accepted translation result into the dialect's own spelling; Supports sees the result before it.
	Native func(Expr) Expr
	// Events maps canonical event values to this dialect's values.
	Events map[string]string
	ID     string
	Vocab  Vocab
	Sem    Semantics
	Class  Class
	// BranchOnPR is true when the branch ref holds the target branch in a pull request run (Woodpecker, Drone).
	// GitLab leaves it unset in a merge request pipeline.
	BranchOnPR bool
}

// caseFlag is the inline flag every translator-made pattern starts with.
const caseFlag = "(?i)"

// Mode is the regex shape a translated pattern takes; the shapes are the only ones the translator
// emits or recognises, which keeps the Lean model small.
type Mode uint8

// Pattern shapes: ^s$, ^s, s$, s.
const (
	ModeExact Mode = iota
	ModePrefix
	ModeSuffix
	ModeSub
)

// Pattern builds the case-insensitive pattern of shape m for the literal s.
func Pattern(m Mode, s string) string {
	p := caseFlag
	if m == ModeExact || m == ModePrefix {
		p += "^"
	}
	p += regexp.QuoteMeta(s)
	if m == ModeExact || m == ModeSuffix {
		p += "$"
	}
	return p
}

// ParsePattern is the inverse of Pattern: it recognises exactly the patterns Pattern builds.
func ParsePattern(p string) (Mode, string, bool) {
	rest, ok := strings.CutPrefix(p, caseFlag)
	if !ok {
		return 0, "", false
	}
	head, tail := false, false
	if r, ok := strings.CutPrefix(rest, "^"); ok {
		rest, head = r, true
	}
	var sb strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == '\\' && i+1 < len(rest) && isMeta(rest[i+1]):
			i++
			sb.WriteByte(rest[i])
		case c == '$' && i == len(rest)-1:
			tail = true
		case isMeta(c):
			return 0, "", false
		default:
			sb.WriteByte(c)
		}
	}
	m := ModeSub
	switch {
	case head && tail:
		m = ModeExact
	case head:
		m = ModePrefix
	case tail:
		m = ModeSuffix
	}
	return m, sb.String(), sb.Len() > 0
}

func isMeta(c byte) bool { return strings.IndexByte(`\.+*?()|[]{}^$`, c) >= 0 }

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// okLit: a literal the case-insensitive regex can stand for (non-empty, ASCII). A backslash is
// refused: GitLab patterns cannot spell a trailing one and read `\/` as a slash.
func okLit(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 || s[i] == '\\' {
			return false
		}
	}
	return true
}

// okEq additionally excludes literals GHA could read as a number: null == '0' is true there.
func okEq(s string) bool { return okLit(s) && s[0] != '-' && (s[0] < '0' || s[0] > '9') }

// okAff: GitLab rejects single-character patterns, so substring tests need two characters.
func okAff(m Mode, s string) bool { return okLit(s) && (m != ModeSub || len(s) >= 2) }

func okFor(m Mode, s string) bool {
	if m == ModeExact {
		return okEq(s)
	}
	return okLit(s)
}

func isRef(e Expr, name string) bool {
	r, ok := e.(Ref)
	return ok && strings.Join(r.Path, ".") == name
}

func isStr(e Expr) (string, bool) {
	l, ok := e.(Lit)
	return l.Str, ok && l.Kind == LitStr
}

// subject reports whether e is a plain string ref the translator may compare: ref_name, sha or event.
func subject(e Expr) (Ref, bool) {
	r, ok := e.(Ref)
	if !ok {
		return Ref{}, false
	}
	switch strings.Join(r.Path, ".") {
	case CtxRefName, CtxSHA, CtxEvent:
		return r, true
	}
	return Ref{}, false
}

// refLit splits a comparison into its ref side and string literal side, in either operand order.
func refLit(l, r Expr) (Expr, string, bool) {
	if s, ok := isStr(r); ok {
		return l, s, true
	}
	if s, ok := isStr(l); ok {
		return r, s, true
	}
	return nil, "", false
}

// boolValued reports whether e always evaluates to a boolean.
func boolValued(e Expr) bool {
	switch v := e.(type) {
	case Unary:
		return true
	case Binary:
		switch v.Op {
		case OpEq, OpNe, OpMatch, OpNoMatch:
			return true
		case OpAnd, OpOr:
			return boolValued(v.L) && boolValued(v.R)
		}
	case Call:
		return affixMode(v) != nil
	}
	return false
}

var affixModes = map[string]Mode{"startsWith": ModePrefix, "endsWith": ModeSuffix, "contains": ModeSub}

func affixMode(c Call) *Mode {
	m, ok := affixModes[c.Name]
	if !ok || len(c.Args) != 2 {
		return nil
	}
	return &m
}

func eventFrom(d Dialect, lit string) (string, bool) {
	l := asciiLower(lit)
	canons := make([]string, 0, len(d.Events))
	for canon := range d.Events {
		canons = append(canons, canon)
	}
	sort.Strings(canons)
	for _, canon := range canons {
		if d.Events[canon] == l {
			return canon, true
		}
	}
	return "", false
}

func rx(neg bool, x Expr, m Mode, s string) Expr {
	op := OpMatch
	if neg {
		op = OpNoMatch
	}
	return Binary{Op: op, L: x, R: Lit{Kind: LitStr, Str: Pattern(m, s)}}
}

// supported reports whether the target dialect accepts t.
func supported(t Expr, to Dialect) bool { return to.Supports == nil || to.Supports(t) }

// reads reports whether e lies in the subset of from that shares its class semantics.
func reads(e Expr, from Dialect) bool { return from.Reads == nil || from.Reads(e) }

func native(t Expr, to Dialect) Expr {
	if to.Native == nil {
		return t
	}
	return to.Native(t)
}

// Translate converts e between two dialects, exactly. Dialects of one class (ClassGHA or ClassGitLab) differ
// only in the names their vocabularies give the canonical refs, so e is returned as it is, provided both
// vocabularies name every ref in it (lean4/Pipemaster/TranslateClass.lean: rename_sound). Across classes the
// rules below apply and the result must be nameable in the target vocabulary. Either way: on every environment related by the
// vocabulary mapping the result has the same truthiness as e under the target's semantics, and the same
// boolean where both are boolean-valued. It returns false exactly when e is not Translatable.
//
// Rules (proved in lean4/Pipemaster/Translate.lean as tr_sound, tr_sound_rev, tr_none_iff, tr_none_iff_rev):
//   - GHA compares strings case-insensitively and GitLab does not, so a ref_name, sha or event ref
//     compared with a string literal by ==, !=, startsWith, endsWith or contains becomes a /i regex.
//     Two non-literal sides are untranslatable.
//   - Event literals map through the dialects' Events tables; an unknown literal is untranslatable.
//   - GHA ref_type == 'branch' && <test on ref_name> becomes a test on the GitLab branch ref (tag likewise);
//     a ref_type or ref outside that shape has no GitLab name.
//   - The left operand of && and || must be boolean-valued: GitLab tests operands with Ruby truthiness
//     ("" is truthy) while GHA treats "" as falsy.
//   - GitLab == and != are case-sensitive: they have no exact GHA form (see TranslateApprox).
//
// The model is ASCII-only: literals with a non-ASCII byte are untranslatable, and the proofs do not cover
// Unicode case folding of environment values, array values or number coercion. A literal with a backslash
// is untranslatable because GitLab patterns cannot spell a trailing one and read `\/` as a slash.
//
// Environment assumptions of the model (the "related by the vocabulary mapping" relation):
//   - Both sides see the same ref: GHA ref_name equals GitLab CI_COMMIT_REF_NAME.
//   - Merge-request and pull_request pipelines are not modelled here: GitLab leaves CI_COMMIT_BRANCH unset
//     there, GHA ref_name is N/merge, and Woodpecker holds the target branch. TranslateFacts states them (it is
//     Translate plus the rewrites and refusals of expr/translate_facts.go), and every emitter calls it.
//   - Case folding is ASCII only.
//   - Only the four modelled events (push, pull_request, schedule, workflow_dispatch) exist; an event
//     literal without an entry in both dialects' Events tables is untranslatable.
func Translate(e Expr, from, to Dialect) (Expr, bool) {
	var t Expr
	var ok bool
	switch {
	case from.Class == 0 || to.Class == 0 || !reads(e, from):
		return nil, false
	case from.Class == to.Class:
		t, ok = renameClass(e, from, to)
	case from.Class == ClassGHA:
		t, ok = toGitLab(e, from, to)
		ok = ok && from.names(e) && to.names(t) && supported(t, to)
		t = native(t, to)
	default:
		t, ok = toGHA(e, from, to)
		ok = ok && from.names(e) && to.names(t) && supported(t, to)
	}
	if !ok {
		return nil, false
	}
	return t, true
}

// Translatable reports whether e translates exactly; it is true exactly when Translate succeeds. The class
// rules are stated separately from Translate, as in the Lean model, so tests can hold the two together.
func Translatable(e Expr, from, to Dialect) bool {
	switch {
	case from.Class == 0 || to.Class == 0 || !reads(e, from):
		return false
	case from.Class == to.Class:
		_, ok := renameClass(e, from, to)
		return ok
	case from.Class == ClassGHA:
		if !okFwd(e, from, to) || !from.names(e) {
			return false
		}
		t, _ := toGitLab(e, from, to)
		return to.names(t) && supported(t, to)
	}
	if !okRev(e, from) || !from.names(e) {
		return false
	}
	t, _ := toGHA(e, from, to)
	return to.names(t) && supported(t, to)
}

func copyRef(r Ref) Ref { return Ref{Path: append([]string(nil), r.Path...)} }

func ctxRef(name string) Ref { return Ref{Path: strings.Split(name, ".")} }

// typeGuard recognises ref_type == 'branch' or 'tag' (either operand order, any case) and returns the
// canonical name of the guarded ref.
func typeGuard(e Expr) (string, bool) {
	b, ok := e.(Binary)
	if !ok || b.Op != OpEq {
		return "", false
	}
	x, lit, ok := refLit(b.L, b.R)
	if !ok || !isRef(x, CtxRefType) {
		return "", false
	}
	switch asciiLower(lit) {
	case "branch":
		return CtxBranch, true
	case "tag":
		return CtxTag, true
	}
	return "", false
}

// refSide returns the ref and string literal of a comparison in either operand order.
func refSide(l, r Expr) (Ref, string, bool) {
	var x Expr
	var s string
	switch ll, lok := l.(Ref); {
	case lok:
		rs, ok := isStr(r)
		if !ok {
			return Ref{}, "", false
		}
		x, s = ll, rs
	default:
		ls, ok := isStr(l)
		rr, rok := r.(Ref)
		if !ok || !rok {
			return Ref{}, "", false
		}
		x, s = rr, ls
	}
	return x.(Ref), s, true
}

func pathName(r Ref) string { return strings.Join(r.Path, ".") }

func toGitLab(e Expr, from, to Dialect) (Expr, bool) {
	switch v := e.(type) {
	case Ref:
		if _, ok := subject(v); ok {
			return copyRef(v), true
		}
	case Binary:
		return binToGitLab(v, from, to)
	case Call:
		return callToGitLab(v)
	case Unary:
		if v.Op != OpNot {
			return nil, false
		}
		if x, ok := toGitLab(v.X, from, to); ok {
			return Unary{Op: OpNot, X: x}, true
		}
	}
	return nil, false
}

func cmpToGitLab(neg bool, l, r Expr, from, to Dialect) (Expr, bool) {
	ref, lit, ok := refSide(l, r)
	if !ok {
		return nil, false
	}
	switch pathName(ref) {
	case CtxEvent:
		canon, ok := eventFrom(from, lit)
		if !ok {
			return nil, false
		}
		tgt, ok := to.Events[canon]
		if !ok {
			return nil, false
		}
		return rx(neg, copyRef(ref), ModeExact, tgt), true
	case CtxRefName, CtxSHA:
		if okEq(lit) {
			return rx(neg, copyRef(ref), ModeExact, lit), true
		}
	}
	return nil, false
}

func callToGitLab(c Call) (Expr, bool) {
	m := affixMode(c)
	if m == nil {
		return nil, false
	}
	ref, ok := c.Args[0].(Ref)
	lit, lok := isStr(c.Args[1])
	if !ok || !lok || !okAff(*m, lit) {
		return nil, false
	}
	if n := pathName(ref); n != CtxRefName && n != CtxSHA {
		return nil, false
	}
	return rx(false, copyRef(ref), *m, lit), true
}

func binToGitLab(b Binary, from, to Dialect) (Expr, bool) {
	switch b.Op {
	case OpEq, OpNe:
		return cmpToGitLab(b.Op == OpNe, b.L, b.R, from, to)
	case OpAnd:
		if g, ok := typeGuard(b.L); ok {
			if r, ok := toGitLab(b.R, from, to); ok {
				if m, ok := r.(Binary); ok && m.Op == OpMatch && isRef(m.L, CtxRefName) {
					return Binary{Op: OpMatch, L: ctxRef(g), R: m.R}, true
				}
			}
		}
		return joinToGitLab(b, from, to)
	case OpOr:
		return joinToGitLab(b, from, to)
	}
	return nil, false
}

func joinToGitLab(b Binary, from, to Dialect) (Expr, bool) {
	if !boolValued(b.L) {
		return nil, false
	}
	l, ok := toGitLab(b.L, from, to)
	if !ok {
		return nil, false
	}
	r, ok := toGitLab(b.R, from, to)
	if !ok {
		return nil, false
	}
	return Binary{Op: b.Op, L: l, R: r}, true
}

// okFwd states the gha to gitlab subset without reference to toGitLab.
func okFwd(e Expr, from, to Dialect) bool {
	switch v := e.(type) {
	case Ref:
		_, ok := subject(v)
		return ok
	case Unary:
		return v.Op == OpNot && okFwd(v.X, from, to)
	case Call:
		m := affixMode(v)
		if m == nil {
			return false
		}
		r, ok := v.Args[0].(Ref)
		s, sok := isStr(v.Args[1])
		n := ""
		if ok {
			n = pathName(r)
		}
		return ok && sok && (n == CtxRefName || n == CtxSHA) && okAff(*m, s)
	case Binary:
		switch v.Op {
		case OpEq, OpNe:
			return cmpOK(v.L, v.R, from, to)
		case OpAnd:
			_, g := typeGuard(v.L)
			return (g && nameTest(v.R)) || (boolValued(v.L) && okFwd(v.L, from, to) && okFwd(v.R, from, to))
		case OpOr:
			return boolValued(v.L) && okFwd(v.L, from, to) && okFwd(v.R, from, to)
		}
	}
	return false
}

func cmpOK(l, r Expr, from, to Dialect) bool {
	ref, lit, ok := refSide(l, r)
	if !ok {
		return false
	}
	switch pathName(ref) {
	case CtxEvent:
		canon, ok := eventFrom(from, lit)
		_, mapped := to.Events[canon]
		return ok && mapped
	case CtxRefName, CtxSHA:
		return okEq(lit)
	}
	return false
}

// nameTest: a positive test of ref_name that the branch/tag guard folds into one regex.
func nameTest(e Expr) bool {
	switch v := e.(type) {
	case Binary:
		if v.Op != OpEq {
			return false
		}
		ref, lit, ok := refSide(v.L, v.R)
		return ok && pathName(ref) == CtxRefName && okEq(lit)
	case Call:
		m := affixMode(v)
		if m == nil {
			return false
		}
		r, ok := v.Args[0].(Ref)
		s, sok := isStr(v.Args[1])
		return ok && sok && pathName(r) == CtxRefName && okAff(*m, s)
	}
	return false
}

func predE(m Mode, x Expr, s string) Expr {
	lit := Lit{Kind: LitStr, Str: s}
	switch m {
	case ModeExact:
		return Binary{Op: OpEq, L: x, R: lit}
	case ModePrefix:
		return Call{Name: "startsWith", Args: []Expr{x, lit}}
	case ModeSuffix:
		return Call{Name: "endsWith", Args: []Expr{x, lit}}
	}
	return Call{Name: "contains", Args: []Expr{x, lit}}
}

func negE(neg bool, p Expr) Expr {
	if neg {
		return Unary{Op: OpNot, X: p}
	}
	return p
}

func guardE(t string, p Expr) Expr {
	typ := Lit{Kind: LitStr, Str: refTypeBranch}
	if t == CtxTag {
		typ.Str = refTypeTag
	}
	return Binary{Op: OpAnd, L: Binary{Op: OpEq, L: ctxRef(CtxRefType), R: typ}, R: p}
}

const (
	refTypeBranch = "branch"
	refTypeTag    = "tag"
)

func toGHA(e Expr, from, to Dialect) (Expr, bool) {
	switch v := e.(type) {
	case Ref:
		switch n := pathName(v); n {
		case CtxRefName, CtxSHA, CtxEvent:
			return copyRef(v), true
		case CtxBranch, CtxTag:
			return guardE(n, ctxRef(CtxRefName)), true
		}
	case Unary:
		if v.Op != OpNot {
			return nil, false
		}
		if x, ok := toGHA(v.X, from, to); ok {
			return Unary{Op: OpNot, X: x}, true
		}
	case Binary:
		switch v.Op {
		case OpMatch, OpNoMatch:
			return rxToGHA(v, from)
		case OpAnd, OpOr:
			if !boolValued(v.L) {
				return nil, false
			}
			l, ok := toGHA(v.L, from, to)
			if !ok {
				return nil, false
			}
			r, ok := toGHA(v.R, from, to)
			if !ok {
				return nil, false
			}
			return Binary{Op: v.Op, L: l, R: r}, true
		}
	}
	return nil, false
}

func rxParts(b Binary) (Ref, Mode, string, bool) {
	ref, ok := b.L.(Ref)
	pat, pok := isStr(b.R)
	if !ok || !pok {
		return Ref{}, 0, "", false
	}
	m, s, ok := ParsePattern(pat)
	return ref, m, s, ok
}

func rxToGHA(b Binary, from Dialect) (Expr, bool) {
	ref, m, s, ok := rxParts(b)
	if !ok || !rxOK(ref, m, s, from) {
		return nil, false
	}
	neg := b.Op == OpNoMatch
	name := ctxRef(CtxRefName)
	switch n := pathName(ref); n {
	case CtxEvent:
		canon, _ := eventFrom(from, s)
		return negE(neg, predE(ModeExact, copyRef(ref), canon)), true
	case CtxRefName, CtxSHA:
		return negE(neg, predE(m, copyRef(ref), s)), true
	default:
		return negE(neg, guardE(n, predE(m, name, s))), true
	}
}

func rxOK(ref Ref, m Mode, s string, from Dialect) bool {
	switch pathName(ref) {
	case CtxEvent:
		_, ok := eventFrom(from, s)
		return m == ModeExact && ok
	case CtxRefName, CtxSHA, CtxBranch, CtxTag:
		return okFor(m, s)
	}
	return false
}

// okRev states the gitlab to gha subset without reference to toGHA.
func okRev(e Expr, from Dialect) bool {
	switch v := e.(type) {
	case Ref:
		switch pathName(v) {
		case CtxRefName, CtxSHA, CtxEvent, CtxBranch, CtxTag:
			return true
		}
	case Unary:
		return v.Op == OpNot && okRev(v.X, from)
	case Binary:
		switch v.Op {
		case OpMatch, OpNoMatch:
			ref, m, s, ok := rxParts(v)
			return ok && rxOK(ref, m, s, from)
		case OpAnd, OpOr:
			return boolValued(v.L) && okRev(v.L, from) && okRev(v.R, from)
		}
	}
	return false
}

// Caveats attached to an approximated GitLab comparison. Each names a way the GHA result can differ.
const (
	ciCaveat      = "GitHub compares strings case-insensitively"
	nullCaveat    = "GitHub treats an unset value as equal to '' (null == '' is true); GitLab does not"
	numberCaveat  = "GitHub coerces a numeric-looking literal to a number, so equality is numeric (null == '0' is true)"
	unicodeCaveat = "GitHub case folding of non-ASCII text may differ from GitLab"
	inputCaveat   = "GitHub sets inputs only on workflow_dispatch and workflow_call runs; on other events the input reads as empty, not as its default"
)

// userRef reports a reference to an input, a matrix axis or a needed job's output: the families that carry a
// user-defined tail, spelled the same in the GHA dialect.
func userRef(r Ref) bool {
	switch {
	case len(r.Path) == 2 && (r.Path[0] == PrefixInputs || r.Path[0] == PrefixMatrix):
		return true
	case len(r.Path) == 4 && r.Path[0] == PrefixNeeds && r.Path[2] == "outputs":
		return true
	}
	return false
}

// TranslateApprox translates what Translate rejects but a close equivalent exists for: a GitLab == or !=
// between a ref and a string literal becomes the GHA == or != (the same test, except for the divergences
// the caveats list), and the caveats say so. Emitters report such a result as emulated. It is NOT covered
// by the Lean theorems: only Translate and Translatable are proved, and tests check this function in Go
// alone. An exact translation returns no caveats.
func TranslateApprox(e Expr, from, to Dialect) (Expr, []string, bool) {
	if _, ok := rewriteFacts(e, from, to, Facts{}); !ok {
		return nil, nil, false
	}
	if t, ok := Translate(e, from, to); ok {
		return t, nil, true
	}
	if !reads(e, from) || from.Class != ClassGitLab || to.Class != ClassGHA {
		return nil, nil, false
	}
	var cav caveats
	t, ok := approxToGHA(e, from, to, &cav)
	if !ok || !from.names(e) || !to.names(t) {
		return nil, nil, false
	}
	return t, cav.list(), true
}

// caveats is an ordered set of caveat strings.
type caveats []string

func (c *caveats) add(s string) {
	for _, x := range *c {
		if x == s {
			return
		}
	}
	*c = append(*c, s)
}

func addAll(c *caveats, xs []string) {
	for _, x := range xs {
		c.add(x)
	}
}

func (c caveats) list() []string { return c }

// litCaveats lists every known divergence between GitLab == lit and GHA == lit for a string value.
func litCaveats(lit string) []string {
	out := []string{ciCaveat}
	if lit == "" {
		out = append(out, nullCaveat)
	}
	if !okEq(lit) && lit != "" && okLit(lit) {
		out = append(out, numberCaveat)
	}
	for i := 0; i < len(lit); i++ {
		if lit[i] >= 0x80 {
			out = append(out, unicodeCaveat)
			break
		}
	}
	return out
}

func approxToGHA(e Expr, from, to Dialect, cav *caveats) (Expr, bool) {
	if t, ok := toGHA(e, from, to); ok {
		return t, true
	}
	switch v := e.(type) {
	case Unary:
		if x, ok := approxToGHA(v.X, from, to, cav); ok && v.Op == OpNot {
			return Unary{Op: OpNot, X: x}, true
		}
	case Binary:
		switch v.Op {
		case OpEq, OpNe:
			return approxCmp(v, from, cav)
		case OpAnd, OpOr:
			if !boolValued(v.L) {
				return nil, false
			}
			l, ok := approxToGHA(v.L, from, to, cav)
			r, rok := approxToGHA(v.R, from, to, cav)
			if ok && rok {
				return Binary{Op: v.Op, L: l, R: r}, true
			}
		}
	}
	return nil, false
}

func approxCmp(b Binary, from Dialect, cav *caveats) (Expr, bool) {
	ref, lit, ok := refSide(b.L, b.R)
	if !ok {
		return nil, false
	}
	if userRef(ref) {
		addAll(cav, litCaveats(lit))
		if ref.Path[0] == PrefixInputs {
			cav.add(inputCaveat)
		}
		return Binary{Op: b.Op, L: copyRef(ref), R: Lit{Kind: LitStr, Str: lit}}, true
	}
	var cmp Expr
	switch n := pathName(ref); n {
	case CtxEvent:
		canon, ok := eventFrom(from, lit)
		if !ok {
			return nil, false
		}
		cav.add(ciCaveat)
		return Binary{Op: b.Op, L: copyRef(ref), R: Lit{Kind: LitStr, Str: canon}}, true
	case CtxRefName, CtxSHA:
		addAll(cav, litCaveats(lit))
		return Binary{Op: b.Op, L: copyRef(ref), R: Lit{Kind: LitStr, Str: lit}}, true
	case CtxBranch, CtxTag:
		addAll(cav, litCaveats(lit))
		cmp = guardE(n, Binary{Op: OpEq, L: ctxRef(CtxRefName), R: Lit{Kind: LitStr, Str: lit}})
	case CtxRef, CtxRepo:
		addAll(cav, litCaveats(lit))
		return Binary{Op: b.Op, L: copyRef(ref), R: Lit{Kind: LitStr, Str: lit}}, true
	case CtxRefType:
		if lit != refTypeBranch && lit != refTypeTag {
			return nil, false
		}
		return Binary{Op: b.Op, L: copyRef(ref), R: Lit{Kind: LitStr, Str: lit}}, true
	default:
		return nil, false
	}
	return negE(b.Op == OpNe, cmp), true
}
