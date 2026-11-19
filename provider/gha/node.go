package gha

import (
	"fmt"
	"math"
	"slices"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const maxAliasHops = 32

// fileParser carries the per-file state every node helper needs; dialect is the family Ext key.
type fileParser struct {
	rep     *report.Report
	lines   map[string]int
	dialect string
	stem    string
	file    string
	d       Dialect
}

func (fp *fileParser) errf(n *yaml.Node, format string, a ...any) error {
	line := 0
	if n != nil {
		line = n.Line
	}
	return fmt.Errorf("%s:%d: %s", fp.file, line, fmt.Sprintf(format, a...))
}

func deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < maxAliasHops; i++ {
		n = n.Alias
	}
	return n
}

type entry struct {
	val  *yaml.Node
	key  string
	line int
}

// mapping reads a YAML mapping and tracks which keys a parser consumed, so the rest can go to Ext.
type mapping struct {
	fp      *fileParser
	n       *yaml.Node
	used    map[string]bool
	native  map[string]any
	entries []entry
}

func (fp *fileParser) mapping(n *yaml.Node, what string) (*mapping, error) {
	n = deref(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, fp.errf(n, "%s must be a mapping", what)
	}
	m := &mapping{fp: fp, n: n, used: map[string]bool{}}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := deref(n.Content[i])
		if k == nil || k.Kind != yaml.ScalarNode {
			return nil, fp.errf(n.Content[i], "%s has a non-scalar key", what)
		}
		m.entries = append(m.entries, entry{key: k.Value, val: deref(n.Content[i+1]), line: k.Line})
	}
	return m, nil
}

// take returns the first value under key and marks it consumed; call release when the form is not modelled.
func (m *mapping) take(key string) *yaml.Node {
	for _, e := range m.entries {
		if e.key == key {
			m.used[key] = true
			return e.val
		}
	}
	return nil
}

func (m *mapping) line(key string) int {
	for _, e := range m.entries {
		if e.key == key {
			return e.line
		}
	}
	return 0
}

func (m *mapping) release(key string) { delete(m.used, key) }

// rest decodes every unconsumed key as plain Go values, or nil when there are none.
func (m *mapping) rest() (map[string]any, error) {
	r, _, err := m.split(nil)
	return r, err
}

// split decodes every unconsumed key as plain Go values, apart into the family keys and those in own.
func (m *mapping) split(own []string) (family, dialect map[string]any, err error) {
	for _, e := range m.entries {
		if m.used[e.key] {
			continue
		}
		v, err := m.fp.plain(e.val)
		if err != nil {
			return nil, nil, err
		}
		dst := &family
		if slices.Contains(own, e.key) {
			dst = &dialect
		}
		if *dst == nil {
			*dst = map[string]any{}
		}
		(*dst)[e.key] = v
	}
	return family, dialect, nil
}

// keep records a provider-native value for Ext that has no unconsumed key behind it.
func (m *mapping) keep(key string, v any) {
	if m.native == nil {
		m.native = map[string]any{}
	}
	m.native[key] = v
}

// ext returns the unconsumed keys plus kept values wrapped for Ext, or nil. Keys named in own belong to
// the dialect alone and go under its ID; the rest go under the family key.
func (m *mapping) ext(own []string) (ir.Ext, error) {
	family, dialect, err := m.split(own)
	if err != nil {
		return nil, err
	}
	if family == nil && m.native != nil {
		family = map[string]any{}
	}
	for k, v := range m.native {
		family[k] = v
	}
	out := ir.Ext{}
	if family != nil {
		out[m.fp.dialect] = family
	}
	if dialect != nil {
		out[m.fp.d.ID] = dialect
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// plain decodes n into maps, slices and scalars that survive a meta round trip.
func (fp *fileParser) plain(n *yaml.Node) (any, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, fp.errf(n, "%v", err)
	}
	return stringKeys(v), nil
}

func stringKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = stringKeys(e)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[fmt.Sprint(k)] = stringKeys(e)
		}
		return out
	case []any:
		for i, e := range t {
			t[i] = stringKeys(e)
		}
		return t
	}
	return v
}

// scalarText gives the text GitHub sees for a scalar: numbers and booleans in their resolved form.
func scalarText(n *yaml.Node) (string, bool) {
	n = deref(n)
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", false
	}
	switch n.Tag {
	case "!!null":
		return "", true
	case "!!bool":
		var b bool
		if n.Decode(&b) == nil {
			return strconv.FormatBool(b), true
		}
	case "!!int":
		var i int64
		if n.Decode(&i) == nil {
			return strconv.FormatInt(i, 10), true
		}
	case "!!float":
		var f float64
		if n.Decode(&f) == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
			return formatFloat(f), true
		}
	}
	return n.Value, true
}

// formatFloat prints like JavaScript's Number toString for the ranges workflows use.
func formatFloat(f float64) string {
	if a := math.Abs(f); a >= 1e21 || (a != 0 && a < 1e-6) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func (fp *fileParser) scalar(n *yaml.Node, what string) (string, error) {
	s, ok := scalarText(n)
	if !ok {
		return "", fp.errf(deref(n), "%s must be a scalar", what)
	}
	return s, nil
}

// stringList reads a scalar or a sequence of scalars; ok is false for any other shape.
func stringList(n *yaml.Node) ([]string, bool) {
	n = deref(n)
	if n == nil {
		return nil, false
	}
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil, true
	}
	if s, ok := scalarText(n); ok {
		return []string{s}, true
	}
	if n.Kind != yaml.SequenceNode {
		return nil, false
	}
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		s, ok := scalarText(c)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func boolNode(n *yaml.Node) (val, ok bool) {
	n = deref(n)
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
		return false, false
	}
	var b bool
	if n.Decode(&b) != nil {
		return false, false
	}
	return b, true
}
