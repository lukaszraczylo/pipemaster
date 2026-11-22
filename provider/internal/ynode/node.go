// Package ynode reads YAML documents into ordered maps that keep key lines, so a provider parser can
// tell which keys it consumed and keep the rest.
package ynode

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	maxNodes = 200000
	maxDepth = 200

	tagMerge = "!!merge"
	tagNull  = "!!null"
)

// Map is a YAML mapping with its key order and key lines; merge keys and aliases are already resolved.
type Map struct {
	Vals  map[string]any
	Lines map[string]int
	// Marks holds the ID marker of the mapping under the key "" and of each key under its name.
	Marks map[string]string
	Order []string
	// Repeated is true when the source mapping repeats a key.
	Repeated bool
}

// NewMap returns an empty mapping.
func NewMap() *Map {
	return &Map{Vals: map[string]any{}, Lines: map[string]int{}, Marks: map[string]string{}}
}

// Repeat is a key that a mapping defines twice.
type Repeat struct {
	Key  string
	Line int
}

// Scal is a scalar; its source text is what substitution and Value literals read.
type Scal struct{ N *yaml.Node }

// Text is the source text of the scalar.
func (s Scal) Text() string { return s.N.Value }

// Null reports a null scalar.
func (s Scal) Null() bool { return s.N.Tag == tagNull }

// Plain decodes the scalar to the Go value Ext holds; a tag the IR cannot marshal stays text.
func (s Scal) Plain() any {
	switch s.N.Tag {
	case "!!str", "!!int", "!!float", "!!bool":
		var v any
		if s.N.Decode(&v) == nil {
			return v
		}
	case tagNull:
		return nil
	}
	return s.N.Value
}

// Builder resolves one document into Map, []any and Scal values within a node budget.
type Builder struct {
	// Skipped lists the lines of "<<" values that are not mappings.
	Skipped []int
	// Repeats lists the keys that a mapping repeats; the last value wins.
	Repeats []Repeat
	nodes   int
}

// Build converts a YAML node.
func (b *Builder) Build(n *yaml.Node, depth int) (any, error) {
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
		return b.Build(n.Content[0], depth+1)
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, fmt.Errorf("line %d: dangling alias", n.Line)
		}
		return b.Build(n.Alias, depth+1)
	case yaml.MappingNode:
		if t, ok := templateVar(n); ok {
			return ScalOf(t), nil
		}
		return b.mapping(n, depth)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := b.Build(c, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return Scal{n}, nil
}

func deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < maxDepth; i++ {
		n = n.Alias
	}
	return n
}

// templateVar reads an unquoted `{{name}}` as the text it stands for: YAML sees a flow mapping whose key is a flow mapping.
func templateVar(n *yaml.Node) (string, bool) {
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 || !isNullScalar(n.Content[1]) {
		return "", false
	}
	k := n.Content[0]
	if k.Kind != yaml.MappingNode || len(k.Content) != 2 || k.Content[0].Kind != yaml.ScalarNode || !isNullScalar(k.Content[1]) {
		return "", false
	}
	return "{{" + k.Content[0].Value + "}}", true
}

func isNullScalar(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == tagNull }

func (b *Builder) mapping(n *yaml.Node, depth int) (*Map, error) {
	m := NewMap()
	if len(n.Content) > 0 {
		m.Marks[""] = idMarker(n.HeadComment, n.Content[0].HeadComment)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := deref(n.Content[i])
		if k == nil || k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: mapping key must be a scalar", n.Content[i].Line)
		}
		if mk := idMarker(k.HeadComment, k.LineComment, n.Content[i+1].LineComment); mk != "" {
			m.Marks[k.Value] = mk
		}
		if k.Tag == tagMerge {
			continue
		}
		v, err := b.Build(n.Content[i+1], depth+1)
		if err != nil {
			return nil, err
		}
		if _, dup := m.Vals[k.Value]; dup {
			b.Repeats = append(b.Repeats, Repeat{Key: k.Value, Line: k.Line})
			m.Repeated = true
			m.Vals[k.Value], m.Lines[k.Value] = v, k.Line
			continue
		}
		m.Set(k.Value, v, k.Line)
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
func (b *Builder) merge(m *Map, v *yaml.Node, depth int) error {
	src, err := b.Build(v, depth+1)
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
		sm, ok := s.(*Map)
		if !ok {
			// A merge source that is not a mapping adds no key; it is dropped and reported.
			b.Skipped = append(b.Skipped, v.Line)
			continue
		}
		for _, k := range sm.Order {
			if _, have := m.Vals[k]; !have {
				m.Set(k, sm.Vals[k], sm.Lines[k])
			}
		}
	}
	return nil
}

// IDPrefix starts the comment that names the source ID of the node it sits on.
const IDPrefix = "pipemaster:id "

// idMarker returns the ID that the first comment with the marker names.
func idMarker(comments ...string) string {
	for _, c := range comments {
		for _, line := range strings.Split(c, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
			if id, ok := strings.CutPrefix(line, IDPrefix); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return ""
}

// Keys lists the keys in source order.
func (m *Map) Keys() []string { return m.Order }

// Get returns the value of a key.
func (m *Map) Get(key string) any { return m.Vals[key] }

// Subset returns the named keys with their values and lines.
func (m *Map) Subset(keys []string) *Map {
	out := NewMap()
	for _, k := range keys {
		out.Set(k, m.Vals[k], m.Lines[k])
	}
	return out
}

// Set appends a key.
func (m *Map) Set(k string, v any, line int) {
	m.Order = append(m.Order, k)
	m.Vals[k] = v
	m.Lines[k] = line
}

// Plain converts a built value to the maps, slices and scalars that Ext holds.
func Plain(v any) any {
	switch t := v.(type) {
	case *Map:
		out := make(map[string]any, len(t.Order))
		for _, k := range t.Order {
			out[k] = Plain(t.Vals[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = Plain(e)
		}
		return out
	case Scal:
		return t.Plain()
	}
	return nil
}

// Reader tracks which keys of a mapping a parser consumed; the rest go to Ext.
type Reader struct {
	M    *Map
	Used map[string]bool
}

// NewReader starts a read of m with no key consumed.
func NewReader(m *Map) *Reader { return &Reader{M: m, Used: map[string]bool{}} }

// Take returns a key's value and marks it consumed.
func (r *Reader) Take(key string) (any, bool) {
	v, ok := r.M.Vals[key]
	if ok {
		r.Used[key] = true
	}
	return v, ok
}

// Release undoes Take for a value whose shape the IR does not model.
func (r *Reader) Release(key string) { delete(r.Used, key) }

// Line is the source line of a key.
func (r *Reader) Line(key string) int { return r.M.Lines[key] }

// Rest lists the keys no parser consumed, in source order.
func (r *Reader) Rest() []string {
	var out []string
	for _, k := range r.M.Order {
		if !r.Used[k] {
			out = append(out, k)
		}
	}
	return out
}

// AsMap reads v as a mapping.
func AsMap(v any) (*Map, bool) {
	m, ok := v.(*Map)
	return m, ok
}

// Text returns a non-null scalar's source text.
func Text(v any) (string, bool) {
	s, ok := v.(Scal)
	if !ok || s.Null() {
		return "", false
	}
	return s.Text(), true
}

// TextList reads a scalar or a list of scalars.
func TextList(v any) ([]string, bool) {
	if t, ok := Text(v); ok {
		return []string{t}, true
	}
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, e := range l {
		t, ok := Text(e)
		if !ok {
			return nil, false
		}
		out = append(out, t)
	}
	return out, true
}

// BoolOf reads a YAML boolean.
func BoolOf(v any) (val, ok bool) {
	s, isScal := v.(Scal)
	if !isScal || s.N.Tag != "!!bool" {
		return false, false
	}
	var b bool
	if s.N.Decode(&b) != nil {
		return false, false
	}
	return b, true
}

// TextOf is Text for a (value, found) pair such as the result of Take.
func TextOf(v any, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	return Text(v)
}

// ScalOf makes a string scalar for a value the parser builds itself.
func ScalOf(s string) Scal { return Scal{N: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}} }
