package gitlab

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	maxRefDepth      = 20
	maxExtendsLevels = 11
	extendsKey       = "extends"
)

// resolveRefs replaces every !reference tag with the node it points to; in a list the target's items are spliced in.
func (s *state) resolveRefs(root *yaml.Node) (*yaml.Node, error) {
	return s.refNode(root, root, nil)
}

func (s *state) refNode(n, root *yaml.Node, stack []string) (*yaml.Node, error) {
	if n.Tag == tagReference {
		return s.followRef(n, root, stack)
	}
	switch n.Kind {
	case yaml.MappingNode:
		out := s.stamp(&yaml.Node{Kind: n.Kind, Tag: n.Tag, Style: n.Style, Line: n.Line, Column: n.Column}, s.org[n])
		for _, e := range entries(n) {
			v, err := s.refNode(e.val, root, stack)
			if err != nil {
				return nil, err
			}
			out.Content = append(out.Content, e.key, v)
		}
		return out, nil
	case yaml.SequenceNode:
		out := s.stamp(&yaml.Node{Kind: n.Kind, Tag: n.Tag, Style: n.Style, Line: n.Line, Column: n.Column}, s.org[n])
		for _, c := range n.Content {
			v, err := s.refNode(c, root, stack)
			if err != nil {
				return nil, err
			}
			if c.Tag == tagReference && v.Kind == yaml.SequenceNode {
				out.Content = append(out.Content, v.Content...)
				continue
			}
			out.Content = append(out.Content, v)
		}
		return out, nil
	}
	return n, nil
}

func (s *state) followRef(n, root *yaml.Node, stack []string) (*yaml.Node, error) {
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return nil, s.errf(n, "!reference needs a list of keys")
	}
	var keys []string
	cur := root
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode {
			return nil, s.errf(c, "!reference keys must be strings")
		}
		keys = append(keys, c.Value)
		cur = get(cur, c.Value)
		if cur == nil {
			return nil, s.errf(n, "!reference [%s] points at nothing", strings.Join(keys, ", "))
		}
	}
	id := strings.Join(keys, "\x00")
	if indexOf(stack, id) >= 0 || len(stack) >= maxRefDepth {
		return nil, s.errf(n, "!reference [%s] is circular or nested too deep", strings.Join(keys, ", "))
	}
	return s.refNode(cur, root, append(stack, id))
}

// jobSet indexes the top-level mappings by name so extends can find a parent.
type jobSet map[string]*yaml.Node

// extend resolves job name against its extends chain: parents merge in order, the job's own keys win,
// and hashes merge while arrays and scalars are replaced.
func (s *state) extend(name string, jobs jobSet, stack []string) (*yaml.Node, error) {
	n := jobs[name]
	if indexOf(stack, name) >= 0 {
		return nil, s.errf(n, "extends cycle: %s", strings.Join(append(append([]string{}, stack...), name), " -> "))
	}
	if len(stack) >= maxExtendsLevels {
		return nil, s.errf(n, "extends goes more than %d levels deep", maxExtendsLevels)
	}
	if n.Kind != yaml.MappingNode {
		return nil, s.errf(n, "extends target %q must be a mapping", name)
	}
	var acc *yaml.Node
	parents, err := s.extendsNames(n)
	if err != nil {
		return nil, err
	}
	for _, p := range parents {
		if _, ok := jobs[p]; !ok {
			return nil, s.errf(get(n, extendsKey), "%s extends unknown job %q", name, p)
		}
		pm, err := s.extend(p, jobs, append(stack, name))
		if err != nil {
			return nil, err
		}
		acc = s.deepMerge(acc, pm)
	}
	own := s.withoutKey(n, extendsKey)
	if acc == nil {
		return own, nil
	}
	return s.deepMerge(acc, own), nil
}

func (s *state) extendsNames(n *yaml.Node) ([]string, error) {
	v := get(n, extendsKey)
	if v == nil {
		return nil, nil
	}
	switch v.Kind {
	case yaml.ScalarNode:
		return []string{v.Value}, nil
	case yaml.SequenceNode:
		var out []string
		for _, c := range v.Content {
			if c.Kind != yaml.ScalarNode {
				return nil, s.errf(c, "extends entries must be job names")
			}
			out = append(out, c.Value)
		}
		return out, nil
	}
	return nil, s.errf(v, "extends must be a job name or a list of job names")
}
