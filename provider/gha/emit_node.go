package gha

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const (
	tagStr  = "!!str"
	tagMap  = "!!map"
	tagSeq  = "!!seq"
	tagNull = "!!null"
	tagBool = "!!bool"
	tagInt  = "!!int"

	lostPrefix = "# pipemaster:lost "
)

// omap builds a mapping node whose key order is the order of set calls; a key set twice keeps its first value.
type omap struct {
	n    *yaml.Node
	keys map[string]bool
}

func newMap() *omap {
	return &omap{n: &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap}, keys: map[string]bool{}}
}

func (m *omap) has(key string) bool { return m.keys[key] }

func (m *omap) empty() bool { return len(m.n.Content) == 0 }

func (m *omap) set(key string, v *yaml.Node) *yaml.Node {
	if m.keys[key] || v == nil {
		return nil
	}
	m.keys[key] = true
	k := str(key)
	m.n.Content = append(m.n.Content, k, v)
	return k
}

// setComment sets key and puts comment lines above it.
func (m *omap) setComment(key string, v *yaml.Node, comments []string) {
	if k := m.set(key, v); k != nil && len(comments) > 0 {
		k.HeadComment = strings.Join(comments, "\n")
	}
}

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: s} }

func nullNode() *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagNull} }

func boolNodeOf(b bool) *yaml.Node {
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

// block renders lines as one literal block scalar; the final newline is the one the parser drops.
func block(lines []string) *yaml.Node {
	if len(lines) == 1 {
		return str(lines[0])
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Style: yaml.LiteralStyle, Value: strings.Join(lines, "\n") + "\n"}
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

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// extMap returns the dialect's own Ext entry as a map, or nil.
func extMap(ext map[string]any, id string) map[string]any {
	m, _ := ext[id].(map[string]any)
	return m
}

// ext returns the Ext entries this emitter writes back: the family's, then its own dialect's.
func (e *emitter) ext(x ir.Ext) map[string]any {
	fam, own := extMap(x, provider.Family(e.d.ID)), extMap(x, e.d.ID)
	if e.d.ID == provider.Family(e.d.ID) || len(own) == 0 {
		return fam
	}
	out := make(map[string]any, len(fam)+len(own))
	maps.Copy(out, fam)
	maps.Copy(out, own)
	return out
}

// addExtra appends every Ext key not in skip and not already set, sorted by key.
func (m *omap) addExtra(ext map[string]any, skip map[string]bool) error {
	for _, k := range sortedKeys(ext) {
		if skip[k] || m.has(k) {
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

func marshalDoc(root *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
