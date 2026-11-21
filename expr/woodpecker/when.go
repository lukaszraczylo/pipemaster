package woodpecker

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// When keys that map to an expression. Each is a doublestar-matched filter except event, which is exact.
const (
	KeyEvent    = "event"
	KeyBranch   = "branch"
	KeyRef      = "ref"
	KeyRepo     = "repo"
	KeyInstance = "instance"
)

// eventTag is the event for which the branch filter does not apply (constraint.go: the branch match is
// skipped when the event is tag).
const eventTag = "tag"

const globPrefix = "when."

// globMeta is doublestar's pattern syntax: a value with none of these matches only itself
// (verified against doublestar v4.10.4 over random patterns); `]` and `}` are included to stay safe.
const globMeta = `*?[]{}\`

// Filter is one when key: values are OR, and Exclude removes matches (List.Match in constraint/list.go).
type Filter struct{ Include, Exclude []string }

// Empty reports whether the filter holds no value.
func (f Filter) Empty() bool { return len(f.Include) == 0 && len(f.Exclude) == 0 }

// merge adds o's halves to the empty halves of f; it reports false, changing nothing, when a half is taken.
func (f *Filter) merge(o Filter) bool {
	if len(f.Include) > 0 && len(o.Include) > 0 || len(f.Exclude) > 0 && len(o.Exclude) > 0 {
		return false
	}
	f.Include = append(f.Include, o.Include...)
	f.Exclude = append(f.Exclude, o.Exclude...)
	return true
}

// Item is one when entry: its keys are ANDed. Path, status, platform, cron and matrix have no expression form
// and stay with the caller.
type Item struct {
	// Evaluate is an expr-lang expression ANDed with the filters.
	Evaluate                           string
	Event, Branch, Ref, Repo, Instance Filter
}

// keyRefs gives the ref each key compares.
var keyRefs = map[string]expr.Ref{
	KeyEvent:    {Path: []string{expr.CtxEvent}},
	KeyBranch:   {Path: []string{expr.CtxBranch}},
	KeyRef:      {Path: []string{expr.CtxRef}},
	KeyRepo:     {Path: []string{expr.CtxRepo}},
	KeyInstance: {Path: []string{Dialect, "CI_SYSTEM_HOST"}},
}

var keyOrder = []string{KeyEvent, KeyBranch, KeyRef, KeyRepo, KeyInstance}

func (it *Item) filter(key string) *Filter {
	switch key {
	case KeyEvent:
		return &it.Event
	case KeyBranch:
		return &it.Branch
	case KeyRef:
		return &it.Ref
	case KeyRepo:
		return &it.Repo
	case KeyInstance:
		return &it.Instance
	}
	return nil
}

// IsLiteral reports whether a filter value holds no glob metacharacter, so it matches only itself.
func IsLiteral(v string) bool { return !strings.ContainsAny(v, globMeta) }

// GlobOpaque is the node for a glob value that has no exact expression form.
func GlobOpaque(key, pattern string) expr.Opaque {
	return expr.Opaque{Dialect: Dialect, Text: globPrefix + key + " glob " + strconv.Quote(pattern)}
}

// ParseGlob inverts GlobOpaque.
func ParseGlob(o expr.Opaque) (key, pattern string, ok bool) {
	rest, ok := strings.CutPrefix(o.Text, globPrefix)
	if !ok || o.Dialect != Dialect {
		return "", "", false
	}
	key, quoted, ok := strings.Cut(rest, " glob ")
	if !ok {
		return "", "", false
	}
	pattern, err := strconv.Unquote(quoted)
	if err != nil || strconv.Quote(pattern) != quoted || IsLiteral(pattern) {
		return "", "", false
	}
	if _, known := keyRefs[key]; !known {
		return "", "", false
	}
	return key, pattern, true
}

func valueExpr(key, v string) expr.Expr {
	if !IsLiteral(v) {
		return GlobOpaque(key, v)
	}
	return expr.Binary{Op: expr.OpEq, L: keyRefs[key], R: expr.Lit{Kind: expr.LitStr, Str: v}}
}

func orChain(key string, vs []string) expr.Expr {
	var out expr.Expr
	for _, v := range vs {
		e := valueExpr(key, v)
		if out == nil {
			out = e
		} else {
			out = expr.Binary{Op: expr.OpOr, L: out, R: e}
		}
	}
	return out
}

func and(l, r expr.Expr) expr.Expr {
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	return expr.Binary{Op: expr.OpAnd, L: l, R: r}
}

func or(l, r expr.Expr) expr.Expr {
	if l == nil || r == nil {
		return nil
	}
	return expr.Binary{Op: expr.OpOr, L: l, R: r}
}

// FilterExpr maps one when key to an expression (include-OR AND NOT exclude-OR, as List.Match in constraint/list.go).
// A literal value is an equality; a glob value is a GlobOpaque. The branch filter does not apply to tag
// events, so it reads `event == "tag" || <filter>`. A nil result means the filter holds no value and matches
// every run. It errors on a key that has no expression form.
func FilterExpr(key string, f Filter) (expr.Expr, error) {
	if _, ok := keyRefs[key]; !ok {
		return nil, fmt.Errorf("woodpecker when: key %q has no expression form", key)
	}
	var out expr.Expr
	if len(f.Include) > 0 {
		out = orChain(key, f.Include)
	}
	if len(f.Exclude) > 0 {
		out = and(out, expr.Unary{Op: expr.OpNot, X: orChain(key, f.Exclude)})
	}
	if key == KeyBranch && out != nil {
		out = or(expr.Binary{Op: expr.OpEq, L: keyRefs[KeyEvent], R: expr.Lit{Kind: expr.LitStr, Str: eventTag}}, out)
	}
	return out, nil
}

// ItemExpr maps one when entry to an expression: the filters in key order, then Evaluate, ANDed. A nil result
// means the entry matches every run.
func ItemExpr(it Item) (expr.Expr, error) {
	var out expr.Expr
	for _, k := range keyOrder {
		e, err := FilterExpr(k, *it.filter(k))
		if err != nil {
			return nil, err
		}
		out = and(out, e)
	}
	if strings.TrimSpace(it.Evaluate) != "" {
		e, err := Parse(it.Evaluate)
		if err != nil {
			return nil, err
		}
		out = and(out, e)
	}
	return out, nil
}

// WhenExpr maps a when list to an expression: entries are ORed. A nil result means the list matches every
// run, which holds for an empty list and for any entry without conditions.
func WhenExpr(items []Item) (expr.Expr, error) {
	var out expr.Expr
	for i, it := range items {
		e, err := ItemExpr(it)
		if err != nil {
			return nil, err
		}
		if e == nil {
			return nil, nil
		}
		if i == 0 {
			out = e
		} else {
			out = expr.Binary{Op: expr.OpOr, L: out, R: e}
		}
	}
	return out, nil
}

func flatten(e expr.Expr, op string) []expr.Expr {
	if b, ok := e.(expr.Binary); ok && b.Op == op {
		return append(flatten(b.L, op), b.R)
	}
	return []expr.Expr{e}
}

// valueOf reads one disjunct as a filter value of some key: an equality of a key ref with a string, or a glob.
func valueOf(e expr.Expr) (key, v string, ok bool) {
	switch x := e.(type) {
	case expr.Opaque:
		return keyAndPattern(x)
	case expr.Binary:
		lit, isLit := x.R.(expr.Lit)
		r, isRef := x.L.(expr.Ref)
		if x.Op != expr.OpEq || !isLit || !isRef || lit.Kind != expr.LitStr || !IsLiteral(lit.Str) {
			return "", "", false
		}
		for _, k := range keyOrder {
			if slices.Equal(keyRefs[k].Path, r.Path) {
				return k, lit.Str, true
			}
		}
	}
	return "", "", false
}

func keyAndPattern(o expr.Opaque) (string, string, bool) {
	k, p, ok := ParseGlob(o)
	return k, p, ok
}

// valuesOf reads an OR chain whose disjuncts all belong to one key.
func valuesOf(e expr.Expr) (string, []string, bool) {
	var key string
	var vs []string
	for _, d := range flatten(e, expr.OpOr) {
		k, v, ok := valueOf(d)
		if !ok || (key != "" && k != key) {
			return "", nil, false
		}
		key = k
		vs = append(vs, v)
	}
	return key, vs, key != ""
}

// filterOf reads include, NOT exclude, or both ANDed.
func filterOf(e expr.Expr) (string, Filter, bool) {
	b, isBin := e.(expr.Binary)
	if isBin && b.Op == expr.OpAnd {
		if n, ok := b.R.(expr.Unary); ok {
			k1, inc, ok1 := valuesOf(b.L)
			k2, exc, ok2 := valuesOf(n.X)
			return k1, Filter{Include: inc, Exclude: exc}, ok1 && ok2 && k1 == k2
		}
		return "", Filter{}, false
	}
	if n, ok := e.(expr.Unary); ok {
		k, exc, ok := valuesOf(n.X)
		return k, Filter{Exclude: exc}, ok
	}
	k, inc, ok := valuesOf(e)
	return k, Filter{Include: inc}, ok
}

// keyFilter reads one conjunct as a when key; branch carries the tag-event guard.
func keyFilter(c expr.Expr) (string, Filter, bool) {
	if b, ok := c.(expr.Binary); ok && b.Op == expr.OpOr {
		guard, isBin := b.L.(expr.Binary)
		lit, isLit := guard.R.(expr.Lit)
		if isBin && isLit && guard.Op == expr.OpEq && isRefPath(guard.L, KeyEvent) && lit.Kind == expr.LitStr && lit.Str == eventTag {
			k, f, ok := filterOf(b.R)
			return k, f, ok && k == KeyBranch
		}
	}
	k, f, ok := filterOf(c)
	return k, f, ok && k != KeyBranch
}

func isRefPath(e expr.Expr, key string) bool {
	r, ok := e.(expr.Ref)
	return ok && slices.Equal(r.Path, keyRefs[key].Path)
}

// SplitItem inverts ItemExpr as far as the shape allows: each conjunct that is a key filter becomes that
// key's Filter, and the rest are ANDed into Evaluate. It returns false when a rest conjunct has no
// expression text. A nil e is an entry without conditions.
func SplitItem(e expr.Expr) (Item, bool) {
	var it Item
	if e == nil {
		return it, true
	}
	var rest expr.Expr
	for _, c := range flatten(e, expr.OpAnd) {
		k, f, ok := keyFilter(c)
		if slot := it.filter(k); ok && slot != nil && slot.merge(f) {
			continue
		}
		rest = and(rest, Boolean(c))
	}
	if rest != nil {
		text, err := Print(rest)
		if err != nil {
			return Item{}, false
		}
		it.Evaluate = text
	}
	return it, true
}

// SplitWhen inverts WhenExpr: the whole expression as one entry when it splits, else each top-level OR
// disjunct as an entry. It returns false when some entry does not split.
func SplitWhen(e expr.Expr) ([]Item, bool) {
	if e == nil {
		return nil, true
	}
	e = Boolean(e)
	if it, ok := SplitItem(e); ok && (it.Evaluate == "" || len(flatten(e, expr.OpOr)) == 1) {
		return []Item{it}, true
	}
	var items []Item
	for _, d := range flatten(e, expr.OpOr) {
		it, ok := SplitItem(d)
		if !ok {
			return nil, false
		}
		items = append(items, it)
	}
	return items, true
}

// StatusExpr maps a when status list to the status call that gates the step: both statuses mean always(),
// failure alone failure(), and success or an absent list success(). It errors on any other value.
func StatusExpr(status []string) (expr.Expr, error) {
	var ok, fail bool
	for _, s := range status {
		switch s {
		case "success":
			ok = true
		case "failure":
			fail = true
		default:
			return nil, fmt.Errorf("woodpecker when: status %q is not success or failure", s)
		}
	}
	name := "success"
	switch {
	case ok && fail:
		name = "always"
	case fail:
		name = "failure"
	}
	return expr.Call{Name: name}, nil
}

// StatusList inverts StatusExpr; success is the absent list.
func StatusList(e expr.Expr) ([]string, bool) {
	c, ok := e.(expr.Call)
	if !ok || len(c.Args) != 0 {
		return nil, false
	}
	switch c.Name {
	case "success":
		return nil, true
	case "failure":
		return []string{"failure"}, true
	case "always":
		return []string{"success", "failure"}, true
	}
	return nil, false
}
