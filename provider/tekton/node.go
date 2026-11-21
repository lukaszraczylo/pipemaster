package tekton

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

// omap is a YAML mapping with its key order and key lines; merge keys and aliases are already resolved.
type omap struct {
	vals  map[string]any
	lines map[string]int
	// marks holds the ID marker of the mapping under the key "" and of each key under its name.
	marks map[string]string
	keys  []string
	// repeated is true when the source mapping repeats a key.
	repeated bool
}

// repeat is a key that a mapping defines twice.
type repeat struct {
	key  string
	line int
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
	// repeats lists the keys that a mapping repeats; the last value wins.
	repeats []repeat
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
		if t, ok := templateVar(n); ok {
			return scalOf(t), nil
		}
		return b.mapping(n, depth)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
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

func (b *builder) mapping(n *yaml.Node, depth int) (*omap, error) {
	m := &omap{vals: map[string]any{}, lines: map[string]int{}, marks: map[string]string{}}
	if len(n.Content) > 0 {
		m.marks[""] = idMarker(n.HeadComment, n.Content[0].HeadComment)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := deref(n.Content[i])
		if k == nil || k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: mapping key must be a scalar", n.Content[i].Line)
		}
		if mk := idMarker(k.HeadComment, k.LineComment, n.Content[i+1].LineComment); mk != "" {
			m.marks[k.Value] = mk
		}
		if k.Tag == tagMerge {
			continue
		}
		v, err := b.build(n.Content[i+1], depth+1)
		if err != nil {
			return nil, err
		}
		if _, dup := m.vals[k.Value]; dup {
			b.repeats = append(b.repeats, repeat{key: k.Value, line: k.Line})
			m.repeated = true
			m.vals[k.Value], m.lines[k.Value] = v, k.Line
			continue
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
			// A merge source that is not a mapping adds no key; it is dropped and reported.
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

// idPrefix starts the comment that names the source ID of the node it sits on.
const idPrefix = "pipemaster:id "

// idMarker returns the ID that the first comment with the marker names.
func idMarker(comments ...string) string {
	for _, c := range comments {
		for _, line := range strings.Split(c, "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
			if id, ok := strings.CutPrefix(line, idPrefix); ok && strings.TrimSpace(id) != "" {
				return strings.TrimSpace(id)
			}
		}
	}
	return ""
}

// Keys lists the keys in source order.
func (m *omap) Keys() []string { return m.keys }

// Get returns the value of a key.
func (m *omap) Get(key string) any { return m.vals[key] }

// subset returns the named keys with their values and lines.
func (m *omap) subset(keys []string) *omap {
	out := &omap{vals: map[string]any{}, lines: map[string]int{}, marks: map[string]string{}}
	for _, k := range keys {
		out.set(k, m.vals[k], m.lines[k])
	}
	return out
}

func (m *omap) set(k string, v any, line int) {
	m.keys = append(m.keys, k)
	m.vals[k] = v
	m.lines[k] = line
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

func textOf(v any, ok bool) (string, bool) {
	if !ok {
		return "", false
	}
	return text(v)
}

// scalOf makes a string scalar for a value the parser builds itself.
func scalOf(s string) scal { return scal{n: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}} }
