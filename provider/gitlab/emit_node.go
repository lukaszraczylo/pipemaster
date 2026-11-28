package gitlab

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	tagStr  = "!!str"
	tagMap  = "!!map"
	tagSeq  = "!!seq"
	tagBool = "!!bool"
	tagInt  = "!!int"

	lostPrefix = "# pipemaster:lost "
	stepPrefix = "# pipemaster:step "
	indentStep = 2
)

// omap builds a mapping node in the order of set calls; a key set twice keeps its first value.
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

func (m *omap) setComment(key string, v *yaml.Node, comments []string) {
	if k := m.set(key, v); k != nil && len(comments) > 0 {
		k.HeadComment = strings.Join(comments, "\n")
	}
}

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: s} }

func boolNodeOf(b bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagBool, Value: strconv.FormatBool(b)}
}

func intNodeOf(i int) *yaml.Node {
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

// block renders a multi-line script entry as a literal block scalar; the final newline is the one the parser drops.
func block(s string) *yaml.Node {
	if !strings.Contains(s, "\n") {
		return str(s)
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Style: yaml.LiteralStyle, Value: s + "\n"}
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

func sortedAny(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// extOf returns the gitlab part of an Ext.
func extOf(e map[string]any) map[string]any {
	m, _ := e[dialect].(map[string]any)
	return emitkit.WithoutCarried(m)
}

// addExtra appends every Ext key not in skip and not already set, sorted by key.
func (m *omap) addExtra(ext map[string]any, skip map[string]bool) error {
	for _, k := range sortedAny(ext) {
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

// mergeExtras adds the raw keys of extra to m without overriding keys already set.
func (m *omap) mergeExtras(extra any) error {
	em, ok := extra.(map[string]any)
	if !ok {
		return nil
	}
	return m.addExtra(em, nil)
}

func marshalDoc(root *yaml.Node) ([]byte, error) {
	if get(root, "include") != nil {
		restoreRefs(root)
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

// markerArg writes v as one marker argument line. JSON keeps it on one line; the open brace of "${{" is
// escaped so a comment never holds a raw expression, and the parser decodes the escape.
func markerArg(v any) (string, error) {
	return emitkit.ArgLine(v)
}

// commentSafe makes s fit on one comment line and removes every "${{", which GitLab would never expand.
func commentSafe(s string) string {
	s = strings.ReplaceAll(s, "${{", "{{")
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(s)
}

var errNotRulesList = errors.New("not a list of rules")

// restoreRefs turns the plain strings that stand for unresolved !reference tags back into tagged lists.
func restoreRefs(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode {
		inner, ok := strings.CutPrefix(n.Value, tagReference+" [")
		inner, ok2 := strings.CutSuffix(inner, "]")
		if !ok || !ok2 || n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return
		}
		n.Kind, n.Tag, n.Value, n.Style = yaml.SequenceNode, tagReference, "", yaml.FlowStyle
		for _, k := range strings.Split(inner, ", ") {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k})
		}
		return
	}
	for _, c := range n.Content {
		restoreRefs(c)
	}
}
