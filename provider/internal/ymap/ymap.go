// Package ymap builds YAML output nodes in a fixed key order for the emitters.
package ymap

import (
	"bytes"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	tagStr  = "!!str"
	tagMap  = "!!map"
	tagSeq  = "!!seq"
	tagBool = "!!bool"
	tagInt  = "!!int"

	indentStep = 2
)

// Map builds a mapping node in the order of Set calls; a key set twice keeps its first value.
type Map struct {
	N    *yaml.Node
	keys map[string]bool
}

// New starts an empty mapping.
func New() *Map {
	return &Map{N: &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap}, keys: map[string]bool{}}
}

// Of wraps a mapping node that already holds the given keys.
func Of(n *yaml.Node, keys ...string) *Map {
	m := &Map{N: n, keys: map[string]bool{}}
	for _, k := range keys {
		m.keys[k] = true
	}
	return m
}

// Drop removes the last key and value that Set added.
func (m *Map) Drop(key string) {
	if !m.keys[key] || len(m.N.Content) < 2 {
		return
	}
	m.N.Content = m.N.Content[:len(m.N.Content)-2]
	delete(m.keys, key)
}

// Has is true when key is set.
func (m *Map) Has(key string) bool { return m.keys[key] }

// Empty is true when no key is set.
func (m *Map) Empty() bool { return len(m.N.Content) == 0 }

// Set adds key once. A nil value is ignored.
func (m *Map) Set(key string, v *yaml.Node) {
	if m.keys[key] || v == nil {
		return
	}
	m.keys[key] = true
	m.N.Content = append(m.N.Content, Str(key), v)
}

// SetStr sets a string value.
func (m *Map) SetStr(key, v string) { m.Set(key, Str(v)) }

// KeyComment puts a head comment above a key that is already set.
func (m *Map) KeyComment(key, comment string) {
	for i := 0; i+1 < len(m.N.Content); i += 2 {
		if m.N.Content[i].Value == key {
			m.N.Content[i].HeadComment = comment
		}
	}
}

// yaml11 matches the plain scalars that a YAML 1.1 reader, such as the one in the Kubernetes API machinery, does not read as a string.
var yaml11 = regexp.MustCompile(`^(?i:y|yes|n|no|on|off|true|false|null|~|=|<<)$` +
	`|^[-+]?(?:0[bBoOxX][0-9a-fA-F_]+|[0-9][0-9_]*(?:\.[0-9_]*)?(?:[eE][-+]?[0-9]+)?|\.[0-9_]+(?:[eE][-+]?[0-9]+)?|[0-9][0-9_]*(?::[0-5]?[0-9])+(?:\.[0-9_]*)?)$` +
	`|^[-+]?\.(?i:inf)$|^\.(?i:nan)$`)

// Ambiguous is true when a plain scalar with this text could read back as a bool, a number or null.
func Ambiguous(s string) bool { return s == "" || yaml11.MatchString(s) }

// Str makes a string scalar. Text that a YAML 1.1 reader would not read as a string is double quoted.
func Str(s string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: s}
	if Ambiguous(s) {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// Bool makes a boolean scalar.
func Bool(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagBool, Value: strconv.FormatBool(b)}
}

// Int makes an integer scalar.
func Int(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagInt, Value: strconv.Itoa(i)}
}

// Seq makes a sequence of items.
func Seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: tagSeq, Content: items}
}

// StrSeq makes a sequence of strings.
func StrSeq(ss []string) *yaml.Node {
	n := Seq()
	for _, s := range ss {
		n.Content = append(n.Content, Str(s))
	}
	return n
}

// Block renders a multi-line script as a literal block scalar.
func Block(s string) *yaml.Node {
	if !strings.Contains(s, "\n") || strings.ContainsAny(s, "\r\t") || strings.HasSuffix(s, " ") || strings.Contains(s, " \n") {
		return Str(s)
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Style: yaml.LiteralStyle, Value: s}
}

// NameValue makes a name and value pair, the form of params and env entries.
func NameValue(name, value string) *yaml.Node {
	m := New()
	m.SetStr("name", name)
	m.SetStr("value", value)
	return m.N
}

// PlainNode encodes a provider-native Ext value; map keys come out sorted so output is stable.
func PlainNode(v any) (n *yaml.Node, err error) {
	defer func() {
		if r := recover(); r != nil {
			n, err = nil, fmt.Errorf("encode ext value: %v", r)
		}
	}()
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1e15 {
		v = int64(f)
	}
	n = &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return nil, fmt.Errorf("encode ext value: %w", err)
	}
	quoteAmbiguous(n)
	return n, nil
}

func quoteAmbiguous(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag == tagStr && n.Style == 0 && Ambiguous(n.Value) {
		n.Style = yaml.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		quoteAmbiguous(c)
	}
}

// SortedKeys lists the keys of m in order.
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AddExtra appends every Ext key that is not in skip and not already set, sorted by key.
func (m *Map) AddExtra(ext map[string]any, skip map[string]bool) error {
	for _, k := range SortedKeys(ext) {
		if skip[k] || strings.HasPrefix(k, "_") || m.Has(k) {
			continue
		}
		n, err := PlainNode(ext[k])
		if err != nil {
			return err
		}
		m.Set(k, n)
	}
	return nil
}

// Doc is one YAML document of an output file with the comment lines above it.
type Doc struct {
	Root *yaml.Node
	Head []string
}

// Marshal writes the documents of one file as a multi-document stream.
func Marshal(docs []*Doc) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indentStep)
	for _, d := range docs {
		if len(d.Head) > 0 {
			d.Root.HeadComment = strings.Join(d.Head, "\n")
		}
		if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{d.Root}}); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
