package gitlab

import (
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	maxNodes = 200000
	maxDepth = 200

	tagMerge     = "!!merge"
	tagReference = "!reference"
	tagNull      = "!!null"
)

// state is shared by one Parse call; org records the file each resolved node came from so errors name it.
type state struct {
	org      map[*yaml.Node]string
	rep      *report.Report
	lines    map[string]position
	nodes    int
	includes int
	remote   bool
}

type position struct {
	file string
	line int
}

func newState(rep *report.Report) *state {
	return &state{org: map[*yaml.Node]string{}, rep: rep, lines: map[string]position{}}
}

func (s *state) errf(n *yaml.Node, format string, a ...any) error {
	return fmt.Errorf("%s: %s", s.loc(n), fmt.Sprintf(format, a...))
}

func (s *state) loc(n *yaml.Node) string {
	if n == nil {
		return "gitlab"
	}
	return fmt.Sprintf("%s:%d", s.org[n], n.Line)
}

// clone copies n with aliases expanded and merge keys flattened, stamping every copy with file.
func (s *state) clone(n *yaml.Node, file string, depth int) (*yaml.Node, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("%s:%d: yaml nesting or alias chain too deep", file, n.Line)
	}
	if s.nodes++; s.nodes > maxNodes {
		return nil, fmt.Errorf("%s:%d: yaml alias expansion too large", file, n.Line)
	}
	switch n.Kind {
	case yaml.AliasNode:
		if n.Alias == nil {
			return nil, fmt.Errorf("%s:%d: dangling alias", file, n.Line)
		}
		return s.clone(n.Alias, file, depth+1)
	case yaml.MappingNode:
		return s.cloneMapping(n, file, depth)
	case yaml.SequenceNode:
		out := s.stamp(&yaml.Node{Kind: n.Kind, Tag: n.Tag, Style: n.Style, Line: n.Line, Column: n.Column, HeadComment: n.HeadComment, FootComment: n.FootComment}, file)
		for _, c := range n.Content {
			cc, err := s.clone(c, file, depth+1)
			if err != nil {
				return nil, err
			}
			out.Content = append(out.Content, cc)
		}
		return out, nil
	}
	// Step markers ride in the comments of script items.
	return s.stamp(&yaml.Node{Kind: n.Kind, Tag: n.Tag, Value: n.Value, Style: n.Style, Line: n.Line, Column: n.Column, HeadComment: n.HeadComment, FootComment: n.FootComment}, file), nil
}

func (s *state) stamp(n *yaml.Node, file string) *yaml.Node {
	s.org[n] = file
	return n
}

func isMergeKey(k *yaml.Node) bool { return k.Kind == yaml.ScalarNode && k.Tag == tagMerge }

func (s *state) cloneMapping(n *yaml.Node, file string, depth int) (*yaml.Node, error) {
	out := s.stamp(&yaml.Node{Kind: yaml.MappingNode, Tag: n.Tag, Style: n.Style, Line: n.Line, Column: n.Column}, file)
	explicit := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i]
		if k.Kind == yaml.AliasNode {
			k = deref(k)
		}
		if k == nil || k.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s:%d: mapping key must be a scalar", file, n.Content[i].Line)
		}
		if isMergeKey(k) {
			continue
		}
		if explicit[k.Value] {
			return nil, fmt.Errorf("%s:%d: duplicate key %q", file, k.Line, k.Value)
		}
		explicit[k.Value] = true
	}
	added := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := deref(n.Content[i])
		if isMergeKey(k) {
			if err := s.mergeInto(out, n.Content[i+1], explicit, added, file, depth); err != nil {
				return nil, err
			}
			continue
		}
		kc, err := s.clone(k, file, depth+1)
		if err != nil {
			return nil, err
		}
		vc, err := s.clone(n.Content[i+1], file, depth+1)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, kc, vc)
	}
	return out, nil
}

// mergeInto adds the pairs of a "<<" value that no explicit key or earlier merge source already defines.
func (s *state) mergeInto(out, v *yaml.Node, explicit, added map[string]bool, file string, depth int) error {
	c, err := s.clone(v, file, depth+1)
	if err != nil {
		return err
	}
	var srcs []*yaml.Node
	switch c.Kind {
	case yaml.MappingNode:
		srcs = []*yaml.Node{c}
	case yaml.SequenceNode:
		srcs = c.Content
	}
	if len(srcs) == 0 && c.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s:%d: merge key value must be a mapping or a list of mappings", file, v.Line)
	}
	for _, src := range srcs {
		if src.Kind != yaml.MappingNode {
			return fmt.Errorf("%s:%d: merge key value must be a mapping or a list of mappings", file, src.Line)
		}
		for i := 0; i+1 < len(src.Content); i += 2 {
			key := src.Content[i].Value
			if explicit[key] || added[key] {
				continue
			}
			added[key] = true
			out.Content = append(out.Content, src.Content[i], src.Content[i+1])
		}
	}
	return nil
}

func deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode && i < maxDepth; i++ {
		n = n.Alias
	}
	return n
}

type entry struct {
	key  *yaml.Node
	val  *yaml.Node
	name string
}

// entries lists the pairs of a resolved mapping in order.
func entries(n *yaml.Node) []entry {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([]entry, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, entry{key: n.Content[i], val: n.Content[i+1], name: n.Content[i].Value})
	}
	return out
}

func get(n *yaml.Node, key string) *yaml.Node {
	for _, e := range entries(n) {
		if e.name == key {
			return e.val
		}
	}
	return nil
}

func isNull(n *yaml.Node) bool { return n != nil && n.Kind == yaml.ScalarNode && n.Tag == tagNull }

// withoutKey returns a mapping without key; the input is not changed.
func (s *state) withoutKey(n *yaml.Node, key string) *yaml.Node {
	out := s.stamp(&yaml.Node{Kind: yaml.MappingNode, Tag: n.Tag, Style: n.Style, Line: n.Line, Column: n.Column}, s.org[n])
	for _, e := range entries(n) {
		if e.name != key {
			out.Content = append(out.Content, e.key, e.val)
		}
	}
	return out
}

// deepMerge merges b over a: two mappings merge key by key at any depth, anything else takes b's value.
func (s *state) deepMerge(a, b *yaml.Node) *yaml.Node {
	if a == nil {
		return b
	}
	if a.Kind != yaml.MappingNode || b.Kind != yaml.MappingNode {
		return b
	}
	out := s.stamp(&yaml.Node{Kind: yaml.MappingNode, Tag: b.Tag, Style: b.Style, Line: b.Line, Column: b.Column}, s.org[b])
	bv := map[string]entry{}
	for _, e := range entries(b) {
		bv[e.name] = e
	}
	done := map[string]bool{}
	for _, e := range entries(a) {
		if be, ok := bv[e.name]; ok {
			out.Content = append(out.Content, be.key, s.deepMerge(e.val, be.val))
			done[e.name] = true
			continue
		}
		out.Content = append(out.Content, e.key, e.val)
	}
	for _, e := range entries(b) {
		if !done[e.name] {
			out.Content = append(out.Content, e.key, e.val)
		}
	}
	return out
}

// plain decodes n into maps, slices and scalars that survive a meta round trip.
func (s *state) plain(n *yaml.Node) (any, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, s.errf(n, "%v", err)
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
