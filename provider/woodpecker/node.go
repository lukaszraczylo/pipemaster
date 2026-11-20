package woodpecker

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"
)

const (
	maxNodes = 200000
	maxDepth = 200

	tagMerge = "!!merge"
	tagNull  = "!!null"
)

// omap is a YAML mapping with its key order and key lines; merge keys and aliases are already resolved.
type omap struct {
	vals  map[string]any
	lines map[string]int
	keys  []string
}

// scal is a scalar; its source text is what substitution and Value literals read.
type scal struct{ n *yaml.Node }

func (s scal) text() string { return s.n.Value }

func (s scal) null() bool { return s.n.Tag == tagNull }

// plain decodes the scalar to the Go value Ext holds; a tag the IR cannot marshal stays text.
func (s scal) plain() any {
	switch s.n.Tag {
	case "!!str", "!!int", "!!float", "!!bool":
		var v any
		if s.n.Decode(&v) == nil {
			return v
		}
	case tagNull:
		return nil
	}
	return s.n.Value
}

// builder resolves one document into omap, []any and scal values within a node budget.
type builder struct {
	// skipped lists the lines of "<<" values that are not mappings.
	skipped []int
	nodes   int
}

func (b *builder) build(n *yaml.Node, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("line %d: yaml nesting or alias chain too deep", n.Line)
	}
	if b.nodes++; b.nodes > maxNodes {
		return nil, fmt.Errorf("line %d: yaml alias expansion too large", n.Line)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return b.build(n.Content[0], depth+1)
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, fmt.Errorf("line %d: dangling alias", n.Line)
		}
		return b.build(n.Alias, depth+1)
	case yaml.MappingNode:
		return b.mapping(n, depth)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			if items, ok, err := b.spliced(c, depth); err != nil {
				return nil, err
			} else if ok {
				out = append(out, items...)
				continue
			}
			v, err := b.build(c, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return scal{n}, nil
}

// spliced reads the sequence form of a merge key that Woodpecker adds to YAML: a list item "<<: *anchor" whose
// value is a list, or a list of lists, stands for the items of that list.
func (b *builder) spliced(c *yaml.Node, depth int) ([]any, bool, error) {
	m := deref(c)
	if m == nil || m.Kind != yaml.MappingNode || len(m.Content) != 2 {
		return nil, false, nil
	}
	if k := deref(m.Content[0]); k == nil || k.Kind != yaml.ScalarNode || k.Tag != tagMerge {
		return nil, false, nil
	}
	v, err := b.build(m.Content[1], depth+1)
	if err != nil {
		return nil, false, err
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false, nil
	}
	var out []any
	for _, e := range list {
		switch t := e.(type) {
		case *omap:
			return nil, false, nil
		case []any:
			out = append(out, t...)
		default:
			out = append(out, e)
		}
	}
	return out, true, nil
}

func deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < maxDepth; i++ {
		n = n.Alias
	}
	return n
}

func (b *builder) mapping(n *yaml.Node, depth int) (*omap, error) {
	m := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := deref(n.Content[i])
		if k == nil || k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: mapping key must be a scalar", n.Content[i].Line)
		}
		if k.Tag == tagMerge {
			continue
		}
		if _, dup := m.vals[k.Value]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q", k.Line, k.Value)
		}
		v, err := b.build(n.Content[i+1], depth+1)
		if err != nil {
			return nil, err
		}
		m.set(k.Value, v, k.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if k := deref(n.Content[i]); k != nil && k.Tag == tagMerge {
			if err := b.merge(m, n.Content[i+1], depth); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}

// merge adds the keys of a "<<" source that the mapping does not already define; earlier sources win.
func (b *builder) merge(m *omap, v *yaml.Node, depth int) error {
	src, err := b.build(v, depth+1)
	if err != nil {
		return err
	}
	var srcs []any
	if l, ok := src.([]any); ok {
		srcs = l
	} else {
		srcs = []any{src}
	}
	for _, s := range srcs {
		sm, ok := s.(*omap)
		if !ok {
			// Woodpecker's linter accepts a sequence of scalars here, so the source is dropped and reported.
			b.skipped = append(b.skipped, v.Line)
			continue
		}
		for _, k := range sm.keys {
			if _, have := m.vals[k]; !have {
				m.set(k, sm.vals[k], sm.lines[k])
			}
		}
	}
	return nil
}

func (m *omap) set(k string, v any, line int) {
	m.keys = append(m.keys, k)
	m.vals[k] = v
	m.lines[k] = line
}

// parseDocument returns the document and the lines of merge sources it could not apply.
func parseDocument(data []byte) (any, []int, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	b := &builder{}
	v, err := b.build(&doc, 0)
	return v, b.skipped, err
}

// plain converts a built value to the maps, slices and scalars that Ext holds.
func plain(v any) any {
	switch t := v.(type) {
	case *omap:
		out := make(map[string]any, len(t.keys))
		for _, k := range t.keys {
			out[k] = plain(t.vals[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = plain(e)
		}
		return out
	case scal:
		return t.plain()
	}
	return nil
}

// reader tracks which keys of a mapping a parser consumed; the rest go to Ext.
type reader struct {
	m    *omap
	used map[string]bool
}

func newReader(m *omap) *reader { return &reader{m: m, used: map[string]bool{}} }

func (r *reader) take(key string) (any, bool) {
	v, ok := r.m.vals[key]
	if ok {
		r.used[key] = true
	}
	return v, ok
}

// release undoes take for a value whose shape the IR does not model.
func (r *reader) release(key string) { delete(r.used, key) }

func (r *reader) line(key string) int { return r.m.lines[key] }

// rest lists the keys no parser consumed, in source order.
func (r *reader) rest() []string {
	var out []string
	for _, k := range r.m.keys {
		if !r.used[k] {
			out = append(out, k)
		}
	}
	return out
}

func asMap(v any) (*omap, bool) {
	m, ok := v.(*omap)
	return m, ok
}

// text returns a non-null scalar's source text.
func text(v any) (string, bool) {
	s, ok := v.(scal)
	if !ok || s.null() {
		return "", false
	}
	return s.text(), true
}

// textList reads a scalar or a list of scalars.
func textList(v any) ([]string, bool) {
	if t, ok := text(v); ok {
		return []string{t}, true
	}
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, e := range l {
		t, ok := text(e)
		if !ok {
			return nil, false
		}
		out = append(out, t)
	}
	return out, true
}

func boolOf(v any) (val, ok bool) {
	s, isScal := v.(scal)
	if !isScal || s.n.Tag != "!!bool" {
		return false, false
	}
	var b bool
	if s.n.Decode(&b) != nil {
		return false, false
	}
	return b, true
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
