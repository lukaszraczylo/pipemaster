package woodpecker

import (
	"bytes"
	"fmt"
	"math"
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

// block renders a multi-line command as a literal block scalar; the final newline is the one the parser drops.
func block(s string) *yaml.Node {
	if !strings.Contains(s, "\n") {
		return str(s)
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Style: yaml.LiteralStyle, Value: s + "\n"}
}

// scalarOf writes a setting value: a plain boolean or integer keeps its YAML type, any other text stays a string.
func scalarOf(s string) *yaml.Node {
	if s == "true" || s == "false" {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagBool, Value: s}
	}
	if n, err := strconv.Atoi(s); err == nil && strconv.Itoa(n) == s {
		return intNode(n)
	}
	return str(s)
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

// extOf returns the woodpecker part of an Ext.
func extOf(e map[string]any) map[string]any {
	m, _ := e[dialect].(map[string]any)
	return m
}

// addExtra appends every Ext key that is not bookkeeping, not in skip and not already set, sorted by key.
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

// mergeMap adds the keys of extra that m lacks; extra is an Ext map value.
func (m *wmap) mergeMap(extra any) error {
	em, ok := extra.(map[string]any)
	if !ok {
		return nil
	}
	return m.addExtra(em, nil)
}

func marshalDoc(root *yaml.Node, head []string) ([]byte, error) {
	if len(head) > 0 && len(root.Content) > 0 {
		root.Content[0].HeadComment = strings.Join(head, "\n")
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indentStep)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// commentSafe makes s fit on one comment line and removes every "${{", which Woodpecker never expands.
func commentSafe(s string) string {
	s = strings.ReplaceAll(s, "${{", "{{")
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}
