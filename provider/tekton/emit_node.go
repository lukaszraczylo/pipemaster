package tekton

import (
	"bytes"
	"fmt"
	"math"
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

// wmap builds a mapping node in the order of set calls; a key set twice keeps its first value.
type wmap struct {
	n    *yaml.Node
	keys map[string]bool
}

func newMap() *wmap {
	return &wmap{n: &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap}, keys: map[string]bool{}}
}

func (m *wmap) has(key string) bool { return m.keys[key] }

func (m *wmap) empty() bool { return len(m.n.Content) == 0 }

func (m *wmap) set(key string, v *yaml.Node) {
	if m.keys[key] || v == nil {
		return
	}
	m.keys[key] = true
	m.n.Content = append(m.n.Content, str(key), v)
}

// setStr sets a string value.
func (m *wmap) setStr(key, v string) { m.set(key, str(v)) }

// keyComment puts a head comment above a key that is already set.
func (m *wmap) keyComment(key, comment string) {
	for i := 0; i+1 < len(m.n.Content); i += 2 {
		if m.n.Content[i].Value == key {
			m.n.Content[i].HeadComment = comment
		}
	}
}

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: s} }

func boolNode(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagBool, Value: strconv.FormatBool(b)}
}

func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagInt, Value: strconv.Itoa(i)}
}

func seqOf(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: tagSeq, Content: items}
}

func strSeq(ss []string) *yaml.Node {
	n := seqOf()
	for _, s := range ss {
		n.Content = append(n.Content, str(s))
	}
	return n
}

// block renders a multi-line script as a literal block scalar.
func block(s string) *yaml.Node {
	if !strings.Contains(s, "\n") {
		return str(s)
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Style: yaml.LiteralStyle, Value: s}
}

// nameValue is a Tekton name/value pair, used for params and env.
func nameValue(name, value string) *yaml.Node {
	m := newMap()
	m.setStr("name", name)
	m.setStr("value", value)
	return m.n
}

// plainNode encodes a provider-native Ext value; map keys come out sorted so output is stable.
func plainNode(v any) (*yaml.Node, error) {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1e15 {
		v = int64(f)
	}
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return nil, fmt.Errorf("encode ext value: %w", err)
	}
	return n, nil
}

// extOf returns the tekton part of an Ext.
func extOf(e map[string]any) map[string]any {
	m, _ := e[dialect].(map[string]any)
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// addExtra appends every Ext key that is not in skip and not already set, sorted by key.
func (m *wmap) addExtra(ext map[string]any, skip map[string]bool) error {
	for _, k := range sortedKeys(ext) {
		if skip[k] || strings.HasPrefix(k, "_") || m.has(k) {
			continue
		}
		n, err := plainNode(ext[k])
		if err != nil {
			return err
		}
		m.set(k, n)
	}
	return nil
}

// outDoc is one YAML document of an output file with the comment lines above it.
type outDoc struct {
	root *yaml.Node
	head []string
}

// marshalDocs writes the documents of one file as a multi-document stream.
func marshalDocs(docs []*outDoc) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indentStep)
	for _, d := range docs {
		if len(d.head) > 0 {
			d.root.HeadComment = strings.Join(d.head, "\n")
		}
		if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{d.root}}); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// commentSafe makes s fit on one comment line.
func commentSafe(s string) string {
	s = strings.ReplaceAll(s, "${{", "{{")
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}
