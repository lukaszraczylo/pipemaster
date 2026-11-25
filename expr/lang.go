package expr

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Class is the evaluation semantics a dialect shares with its siblings. The zero value is no class:
// such a dialect translates nothing.
type Class uint8

// Dialect classes. ClassGHA compares strings case-insensitively and coerces as GHA does. ClassGitLab
// compares case-sensitively and matches with =~ /re/i.
const (
	ClassGHA Class = iota + 1
	ClassGitLab
)

var (
	langMu sync.RWMutex
	langs  = map[string]*Dialect{}
)

// RegisterLang makes d available to LangOf under d.ID. It panics on an empty ID, a zero Class or a duplicate ID.
func RegisterLang(d *Dialect) {
	if d == nil || d.ID == "" || d.Class == 0 {
		panic("expr: RegisterLang needs a dialect with an ID and a Class")
	}
	langMu.Lock()
	defer langMu.Unlock()
	if _, dup := langs[d.ID]; dup {
		panic(fmt.Sprintf("expr: dialect %q registered twice", d.ID))
	}
	langs[d.ID] = d
}

// LangOf returns the dialect registered under id.
func LangOf(id string) (*Dialect, bool) {
	langMu.RLock()
	defer langMu.RUnlock()
	d, ok := langs[id]
	return d, ok
}

// SourceFor returns the dialect to translate from when a project of the given origin is emitted into
// target. It returns nil when the origin's expressions already read in target's language, so the
// emitter keeps them as they are. An unregistered origin gets a classless dialect that translates nothing.
func SourceFor(origin string, target Dialect) *Dialect {
	if origin == "" || origin == target.ID {
		return nil
	}
	from, ok := LangOf(origin)
	if !ok {
		return &Dialect{ID: origin}
	}
	if from.sameLang(target) {
		return nil
	}
	return from
}

// sameLang is true when the dialects differ at most in ID, so no ref or event renames between them.
func (d Dialect) sameLang(o Dialect) bool {
	return d.Class == o.Class && d.BranchOnPR == o.BranchOnPR && d.PRFamily == o.PRFamily && maps.Equal(d.Events, o.Events) &&
		maps.EqualFunc(d.Vocab.ToCanon, o.Vocab.ToCanon, slices.Equal) &&
		maps.EqualFunc(d.Vocab.FromCanon, o.Vocab.FromCanon, slices.Equal)
}

// walkRefs calls fn for every Ref in e until fn returns false.
func walkRefs(e Expr, fn func(Ref) bool) bool {
	switch v := e.(type) {
	case Ref:
		return fn(v)
	case Unary:
		return walkRefs(v.X, fn)
	case Binary:
		return walkRefs(v.L, fn) && walkRefs(v.R, fn)
	case Call:
		for _, a := range v.Args {
			if !walkRefs(a, fn) {
				return false
			}
		}
	}
	return true
}

// names reports whether d's vocabulary names every ref of e.
func (d Dialect) names(e Expr) bool {
	return walkRefs(e, func(r Ref) bool { _, ok := d.Vocab.Dialect(r.Path); return ok })
}

// renameClass is the same-class rule: both vocabularies name every ref, and the target can print the result.
// The canonical name sits between the two, so refs keep their nodes. Event literals are native strings, so
// they map through the Events tables. An Opaque node is dialect text that no vocabulary renames.
func renameClass(e Expr, from, to Dialect, lists bool) (Expr, bool) {
	if HasOpaque(e) || !from.names(e) || !to.names(e) {
		return nil, false
	}
	t, ok := mapEvents(e, from, to)
	if !ok {
		return nil, false
	}
	if lists {
		if t, ok = eventListsRx(t, from, to); !ok {
			return nil, false
		}
	}
	if to.Supports != nil && !to.Supports(t) {
		return nil, false
	}
	return native(t, to), true
}

// mapEvents rewrites each event literal from one Events table to the other (lean4/Pipemaster/TranslateClass.lean:
// event_eq_rename, event_ne_rename, event_rx_rename). Equal tables need no rewrite. Otherwise both tables must be
// injective after lowercasing and every use of the event ref must be a comparison with a literal that the
// source table holds, because any other use could see a value the rewrite does not map.
func mapEvents(e Expr, from, to Dialect) (Expr, bool) {
	if maps.Equal(from.Events, to.Events) {
		return e, true
	}
	if !eventTableOK(from.Events) || !eventTableOK(to.Events) {
		return nil, false
	}
	return mapEventNode(e, from, to)
}

// eventTableOK: values are non-empty and stay distinct after lowercasing, so a literal names one event.
func eventTableOK(t map[string]string) bool {
	seen := make(map[string]bool, len(t))
	for _, v := range t {
		l := asciiLower(v)
		if v == "" || seen[l] {
			return false
		}
		seen[l] = true
	}
	return true
}

func mapEventNode(e Expr, from, to Dialect) (Expr, bool) {
	switch v := e.(type) {
	case Ref:
		return e, pathName(v) != CtxEvent
	case Unary:
		x, ok := mapEventNode(v.X, from, to)
		return Unary{Op: v.Op, X: x}, ok
	case Call:
		args := make([]Expr, len(v.Args))
		for i, a := range v.Args {
			var ok bool
			if args[i], ok = mapEventNode(a, from, to); !ok {
				return nil, false
			}
		}
		return Call{Name: v.Name, Args: args}, true
	case Binary:
		return mapEventBinary(v, from, to)
	}
	return e, true
}

func mapEventBinary(b Binary, from, to Dialect) (Expr, bool) {
	switch {
	case (b.Op == OpEq || b.Op == OpNe) && (isRef(b.L, CtxEvent) || isRef(b.R, CtxEvent)):
		l, r := b.L, b.R
		lit := &r
		if isRef(r, CtxEvent) {
			lit = &l
		}
		s, ok := isStr(*lit)
		if !ok {
			return nil, false
		}
		fold := from.Class == ClassGHA
		t, ok := mapEventLit(from, to, s, fold)
		if !ok {
			return nil, false
		}
		if to.EventRx && from.Class == ClassGitLab {
			// A GitLab-class == compares exactly, so the literal is a value of the source table (mapEventLit fold false)
			// and the /i pattern of the target value names the same event: both tables are injective after lowercasing.
			return rx(b.Op == OpNe, Ref{Path: []string{CtxEvent}}, ModeExact, t), true
		}
		*lit = Lit{Kind: LitStr, Str: t}
		return Binary{Op: b.Op, L: l, R: r}, true
	case (b.Op == OpMatch || b.Op == OpNoMatch) && isRef(b.L, CtxEvent) && from.Class == ClassGitLab:
		pat, ok := isStr(b.R)
		m, s, pok := ParsePattern(pat)
		// A prefix test of the pull request event is how a dialect with several of them names them all (prFamily); a
		// dialect with one names the one.
		family := m == ModePrefix && from.PRFamily && s == from.Events[eventPR]
		if !ok || !pok || (m != ModeExact && !family) {
			return nil, false
		}
		t, ok := mapEventLit(from, to, s, true)
		if !ok {
			return nil, false
		}
		return Binary{Op: b.Op, L: b.L, R: Lit{Kind: LitStr, Str: Pattern(ModeExact, t)}}, true
	}
	l, ok := mapEventNode(b.L, from, to)
	if !ok {
		return nil, false
	}
	r, ok := mapEventNode(b.R, from, to)
	return Binary{Op: b.Op, L: l, R: r}, ok
}

// mapEventLit maps a literal of from's table to the matching entry of to's table. A case-sensitive comparison
// (GitLab ==) needs the exact entry; a case-insensitive one (GHA ==, a /i pattern) matches any case.
func mapEventLit(from, to Dialect, lit string, fold bool) (string, bool) {
	for canon, v := range from.Events {
		if v == lit || fold && asciiLower(v) == asciiLower(lit) {
			t, ok := to.Events[canon]
			return t, ok && okEq(t)
		}
	}
	return "", false
}
