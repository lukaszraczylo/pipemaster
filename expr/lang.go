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
	return d.Class == o.Class && maps.Equal(d.Events, o.Events) &&
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

// renamable is the same-class rule: both vocabularies name every ref, and the target can print the result.
// The canonical name sits between the two, so no node changes. An Opaque node is dialect text that no
// vocabulary renames.
func renamable(e Expr, from, to Dialect) bool {
	return !HasOpaque(e) && from.names(e) && to.names(e) && (to.Supports == nil || to.Supports(e))
}
