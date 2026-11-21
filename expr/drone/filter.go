package drone

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// when keys that map to an expression. Each is a doublestar-matched filter.
const (
	KeyEvent  = "event"
	KeyBranch = "branch"
	KeyRef    = "ref"
	KeyRepo   = "repo"
	KeyStatus = "status"
)

const (
	statusSuccess = "success"
	statusFailure = "failure"
)

// globMeta is doublestar's pattern syntax: a value with none of these matches only itself; `]` and `}` are
// included to stay safe. runner-go imports bmatcuk/doublestar v1.3.4 (go.mod at runner-go 243a9e7).
const globMeta = `*?[]{}\`

const globPrefix = "when."

// tagRefPrefix starts the full ref of a tag; `ref: refs/tags/x` is the tag x.
const tagRefPrefix = "refs/tags/"

// disabledPattern matches every string, so as an event exclude it removes every run. Condition.Match in
// drone/runner-go manifest/cond.go (lines 57-61, commit 243a9e7) returns false for a value that an exclude
// pattern matches, before it reads the include list, and the glob ** matches every string, the empty one too.
const disabledPattern = "**"

// Filter is one when key: values are OR, and Exclude removes matches.
type Filter struct{ Include, Exclude []string }

// Conditions is the part of a Drone when or trigger block that has an expression form. Status holds
// ["failure"] or ["success", "failure"]; nil is the default success-only policy.
type Conditions struct {
	Event, Branch, Ref, Repo Filter
	Status                   []string
}

// Disabled is the conditions that match no run.
func Disabled() Conditions { return Conditions{Event: Filter{Exclude: []string{disabledPattern}}} }

// IsDisabled reports whether c matches no run.
func (c Conditions) IsDisabled() bool { return slices.Contains(c.Event.Exclude, disabledPattern) }

// keyOrder fixes the conjunct order of the expression.
var keyOrder = []string{KeyEvent, KeyBranch, KeyRef, KeyRepo}

var keyRefs = map[string]expr.Ref{
	KeyEvent:  {Path: []string{expr.CtxEvent}},
	KeyBranch: {Path: []string{expr.CtxBranch}},
	KeyRef:    {Path: []string{expr.CtxRef}},
	KeyRepo:   {Path: []string{expr.CtxRepo}},
}

var tagRef = expr.Ref{Path: []string{expr.CtxTag}}

func (c *Conditions) filter(key string) *Filter {
	switch key {
	case KeyEvent:
		return &c.Event
	case KeyBranch:
		return &c.Branch
	case KeyRef:
		return &c.Ref
	case KeyRepo:
		return &c.Repo
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

func strLit(s string) expr.Lit { return expr.Lit{Kind: expr.LitStr, Str: s} }

func valueExpr(key, v string) (expr.Expr, error) {
	if !IsLiteral(v) {
		return GlobOpaque(key, v), nil
	}
	if tag, ok := strings.CutPrefix(v, tagRefPrefix); ok && key == KeyRef && tag != "" {
		return expr.Binary{Op: expr.OpEq, L: tagRef, R: strLit(tag)}, nil
	}
	return expr.Binary{Op: expr.OpEq, L: keyRefs[key], R: strLit(v)}, nil
}

// canonEventList maps Drone event values to canonical names. The pair push and tag is the canonical push, and
// the tag entries then drop out. Push without tag is Drone's push-only event; the AST has no exact node for it
// (it would need a ref test that the filter reader cannot invert), so it is an error and the caller keeps the
// filter native. A glob stays as written.
func canonEventList(vs []string) ([]string, error) {
	hasPush, hasTag := slices.Contains(vs, "push"), slices.Contains(vs, "tag")
	if hasPush && !hasTag {
		return nil, errors.New("drone when: event push without tag has no canonical form")
	}
	var out []string
	for _, v := range vs {
		switch {
		case !IsLiteral(v):
			out = append(out, v)
		case v == "tag" && hasPush:
		case v == "push":
			out = append(out, eventPush)
		default:
			c, ok := canonEvent(v)
			if !ok {
				return nil, fmt.Errorf("drone when: event %q has no canonical name", v)
			}
			out = append(out, c)
		}
	}
	return out, nil
}

func orChain(key string, vs []string) (expr.Expr, error) {
	if key == KeyEvent {
		var err error
		if vs, err = canonEventList(vs); err != nil {
			return nil, err
		}
	}
	var out expr.Expr
	for _, v := range vs {
		e, err := valueExpr(key, v)
		if err != nil {
			return nil, err
		}
		out = or(out, e)
	}
	return out, nil
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
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	return expr.Binary{Op: expr.OpOr, L: l, R: r}
}

// FilterExpr maps one when key to an expression: include-OR AND NOT exclude-OR, as Condition.Match in
// manifest/cond.go. A literal value is an equality, a glob is a GlobOpaque, and `ref: refs/tags/x` is the tag
// x. An event value maps to its canonical name (cron is schedule) and the pair push and tag to push; a value
// with none, and push without tag, is an error.
// Branch carries no tag-event guard: on a pull request Drone matches it against the target branch. A nil
// result means the filter holds no value and matches every run.
func FilterExpr(key string, f Filter) (expr.Expr, error) {
	if _, ok := keyRefs[key]; !ok {
		return nil, fmt.Errorf("drone when: key %q has no expression form", key)
	}
	var out expr.Expr
	var err error
	if len(f.Include) > 0 {
		if out, err = orChain(key, f.Include); err != nil {
			return nil, err
		}
	}
	if len(f.Exclude) > 0 {
		exc, err := orChain(key, f.Exclude)
		if err != nil {
			return nil, err
		}
		out = and(out, expr.Unary{Op: expr.OpNot, X: exc})
	}
	return out, nil
}

// ToExpr maps conditions to an expression: the filters in key order, then the status call when the policy is
// not the default. A nil result means the conditions match every successful run.
func ToExpr(c Conditions) (expr.Expr, error) {
	var out expr.Expr
	for _, k := range keyOrder {
		e, err := FilterExpr(k, *c.filter(k))
		if err != nil {
			return nil, err
		}
		out = and(out, e)
	}
	if len(c.Status) > 0 {
		s, err := statusExpr(c.Status)
		if err != nil {
			return nil, err
		}
		out = and(out, s)
	}
	return out, nil
}

// statusExpr maps a status list: both statuses mean always(), failure alone failure(), success alone success().
func statusExpr(status []string) (expr.Expr, error) {
	var ok, fail bool
	for _, s := range status {
		switch s {
		case statusSuccess:
			ok = true
		case statusFailure:
			fail = true
		default:
			return nil, fmt.Errorf("drone when: status %q is not success or failure", s)
		}
	}
	name := statusSuccess
	switch {
	case ok && fail:
		name = "always"
	case fail:
		name = statusFailure
	}
	return expr.Call{Name: name}, nil
}

// statusList inverts statusExpr; success is the absent list.
func statusList(c expr.Call) ([]string, bool) {
	if len(c.Args) != 0 {
		return nil, false
	}
	switch c.Name {
	case statusSuccess:
		return nil, true
	case statusFailure:
		return []string{statusFailure}, true
	case "always":
		return []string{statusSuccess, statusFailure}, true
	}
	return nil, false
}

// flattenAnd splits only the left spine: ToExpr nests a key's include-AND-NOT-exclude on the right, and that
// pair must stay one conjunct.
func flattenAnd(e expr.Expr) []expr.Expr {
	if b, ok := e.(expr.Binary); ok && b.Op == expr.OpAnd {
		return append(flattenAnd(b.L), b.R)
	}
	return []expr.Expr{e}
}

func flattenOr(e expr.Expr) []expr.Expr {
	if b, ok := e.(expr.Binary); ok && b.Op == expr.OpOr {
		return append(flattenOr(b.L), flattenOr(b.R)...)
	}
	return []expr.Expr{e}
}

// reader reads an AST under the equality semantics of its source dialect.
type reader struct{ sem expr.Semantics }

// valueOf reads one disjunct as a filter value of some key: an equality of a key ref with a string, a tag
// equality, or a glob.
func (rd reader) valueOf(e expr.Expr) (key string, vs []string, ok bool) {
	switch x := e.(type) {
	case expr.Opaque:
		k, p, ok := ParseGlob(x)
		return k, []string{p}, ok
	case expr.Binary:
		lit, isLit := x.R.(expr.Lit)
		r, isRef := x.L.(expr.Ref)
		if x.Op != expr.OpEq || !isLit || !isRef || lit.Kind != expr.LitStr || !IsLiteral(lit.Str) {
			return "", nil, false
		}
		if slices.Equal(r.Path, tagRef.Path) && lit.Str != "" && exactValue(lit.Str, rd.sem) {
			return KeyRef, []string{tagRefPrefix + lit.Str}, true
		}
		for _, k := range keyOrder {
			if !slices.Equal(keyRefs[k].Path, r.Path) {
				continue
			}
			if k == KeyEvent {
				vs, ok := droneEvents(lit.Str, rd.sem.CaseInsensitiveEq)
				return k, vs, ok
			}
			return k, []string{lit.Str}, exactValue(lit.Str, rd.sem)
		}
	}
	return "", nil, false
}

// valuesOf reads an OR chain whose disjuncts all belong to one key.
func (rd reader) valuesOf(e expr.Expr) (string, []string, bool) {
	var key string
	var vs []string
	for _, d := range flattenOr(e) {
		k, v, ok := rd.valueOf(d)
		if !ok || (key != "" && k != key) {
			return "", nil, false
		}
		key = k
		vs = append(vs, v...)
	}
	return key, vs, key != ""
}

// filterOf reads include, NOT exclude, or both ANDed.
func (rd reader) filterOf(e expr.Expr) (string, Filter, bool) {
	if b, ok := e.(expr.Binary); ok && b.Op == expr.OpAnd {
		n, isNot := b.R.(expr.Unary)
		if !isNot || n.Op != expr.OpNot {
			return "", Filter{}, false
		}
		k1, inc, ok1 := rd.valuesOf(b.L)
		k2, exc, ok2 := rd.valuesOf(n.X)
		return k1, Filter{Include: inc, Exclude: exc}, ok1 && ok2 && k1 == k2
	}
	if n, ok := e.(expr.Unary); ok && n.Op == expr.OpNot {
		k, exc, ok := rd.valuesOf(n.X)
		return k, Filter{Exclude: exc}, ok
	}
	k, inc, ok := rd.valuesOf(e)
	return k, Filter{Include: inc}, ok
}

// FromExpr inverts ToExpr. sem is the equality semantics of the dialect that wrote e. Drone globs are case
// sensitive, so under a folding source a comparison becomes a filter value only when its literal has no cased
// character; an event literal is lowered first and maps to its DRONE_BUILD_EVENT value, and one with no Drone
// value (push) fails. It succeeds only for an AND of per-key ORs over branch, tag, event, ref and repo
// refs, with at most one filter per key and an optional status call. Any other shape, including an OR across
// keys, a ref with no Drone filter such as pr.head_ref, or an Opaque that is not a glob, fails: Drone ANDs
// the keys of one when block and has no OR between them. A nil e matches every run.
func FromExpr(e expr.Expr, sem expr.Semantics) (Conditions, bool) {
	rd := reader{sem}
	var c Conditions
	if e == nil {
		return c, true
	}
	if l, ok := e.(expr.Lit); ok && l.Kind == expr.LitBool {
		if l.Bool {
			return c, true
		}
		return Disabled(), true
	}
	seenStatus := false
	for _, conj := range flattenAnd(e) {
		if call, ok := conj.(expr.Call); ok {
			s, ok := statusList(call)
			if !ok || seenStatus {
				return Conditions{}, false
			}
			seenStatus, c.Status = true, s
			continue
		}
		k, f, ok := rd.filterOf(conj)
		if !ok {
			return Conditions{}, false
		}
		slot := c.filter(k)
		switch {
		case len(slot.Include) > 0 && len(slot.Exclude) == 0 && len(f.Include) == 0:
			// The left spine split an include and its exclude of the same key.
			slot.Exclude = f.Exclude
		case len(slot.Include) > 0 || len(slot.Exclude) > 0:
			return Conditions{}, false
		default:
			*slot = f
		}
	}
	return c, true
}

const (
	keyInclude = "include"
	keyExclude = "exclude"
)

// cond decodes one Condition value: a scalar, a list, or an include/exclude map (UnmarshalYAML in
// manifest/cond.go). It rejects the shapes that Drone drops without an error, such as a scalar include,
// because dropping an include turns the filter into match-all.
type cond struct{ Filter }

func scalarText(n *yaml.Node) (string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return "", fmt.Errorf("line %d: want a string value", n.Line)
	}
	return n.Value, nil
}

func resolve(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

func stringList(n *yaml.Node) ([]string, error) {
	n = resolve(n)
	if n.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("line %d: want a list of strings", n.Line)
	}
	var out []string
	for _, item := range n.Content {
		v, err := scalarText(resolve(item))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *cond) UnmarshalYAML(n *yaml.Node) error {
	n = resolve(n)
	switch n.Kind {
	case yaml.ScalarNode:
		v, err := scalarText(n)
		c.Include = []string{v}
		return err
	case yaml.SequenceNode:
		var err error
		c.Include, err = stringList(n)
		return err
	case yaml.MappingNode:
		return c.mapping(n)
	}
	return fmt.Errorf("line %d: want a string, a list or an include/exclude map", n.Line)
}

func (c *cond) mapping(n *yaml.Node) error {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, err := scalarText(resolve(n.Content[i]))
		if err != nil || seen[key] {
			return fmt.Errorf("line %d: bad or repeated key", n.Content[i].Line)
		}
		seen[key] = true
		switch key {
		case keyInclude:
			c.Include, err = stringList(n.Content[i+1])
		case keyExclude:
			c.Exclude, err = stringList(n.Content[i+1])
		default:
			err = fmt.Errorf("line %d: key %q is not include or exclude", n.Content[i].Line, key)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func normaliseStatus(f Filter) ([]string, error) {
	if len(f.Exclude) > 0 {
		return nil, errors.New("drone when: status takes no exclude list")
	}
	var ok, fail bool
	for _, s := range f.Include {
		switch s {
		case statusSuccess:
			ok = true
		case statusFailure:
			fail = true
		default:
			return nil, fmt.Errorf("drone when: status %q is not success or failure", s)
		}
	}
	switch {
	case ok && fail:
		return []string{statusSuccess, statusFailure}, nil
	case fail:
		return []string{statusFailure}, nil
	}
	return nil, nil
}

// Parse decodes a Drone when or trigger mapping. It returns the keys it does not model (action, cron,
// instance, paths, target), sorted, so the caller can warn about them.
func Parse(data []byte) (Conditions, []string, error) {
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Conditions{}, nil, err
	}
	var c Conditions
	var rest []string
	for _, k := range slices.Sorted(maps.Keys(raw)) {
		n := raw[k]
		var v cond
		if k != KeyStatus && c.filter(k) == nil {
			rest = append(rest, k)
			continue
		}
		if err := v.UnmarshalYAML(&n); err != nil {
			return Conditions{}, nil, fmt.Errorf("drone when %s: %w", k, err)
		}
		if k == KeyStatus {
			s, err := normaliseStatus(v.Filter)
			if err != nil {
				return Conditions{}, nil, err
			}
			c.Status = s
			continue
		}
		*c.filter(k) = v.Filter
	}
	return c, rest, nil
}

// value is the shortest YAML form of f that Parse reads back: a scalar, a list, or an include/exclude map.
func (f Filter) value() any {
	if len(f.Exclude) > 0 {
		m := map[string]any{keyExclude: f.Exclude}
		if len(f.Include) > 0 {
			m[keyInclude] = f.Include
		}
		return m
	}
	if len(f.Include) == 1 {
		return f.Include[0]
	}
	return f.Include
}

// YAML is the when mapping for c, ready to marshal. Keys with no value are absent.
func (c Conditions) YAML() map[string]any {
	out := map[string]any{}
	for _, k := range keyOrder {
		if f := *c.filter(k); len(f.Include) > 0 || len(f.Exclude) > 0 {
			out[k] = f.value()
		}
	}
	if len(c.Status) > 0 {
		out[KeyStatus] = c.Status
	}
	return out
}
