package gitlab

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// variables reads a variables mapping; extras holds the description, options and expand keys of the hash form by name.
func (s *state) variables(n *yaml.Node) (vars []ir.Var, extras map[string]any, err error) {
	if n.Kind != yaml.MappingNode {
		return nil, nil, s.errf(n, "variables must be a mapping")
	}
	for _, e := range entries(n) {
		v := ir.Var{Name: e.name}
		switch e.val.Kind {
		case yaml.ScalarNode:
			v.Value.Lit, _ = scalarText(e.val)
		case yaml.MappingNode:
			r, err := s.reader(e.val, "variable "+e.name)
			if err != nil {
				return nil, nil, err
			}
			if val := r.take("value"); val != nil {
				if v.Value.Lit, err = s.scalar(val, "variable "+e.name+" value"); err != nil {
					return nil, nil, err
				}
			}
			x, err := r.ext()
			if err != nil {
				return nil, nil, err
			}
			if x != nil {
				if extras == nil {
					extras = map[string]any{}
				}
				extras[e.name] = x[dialect]
			}
		default:
			return nil, nil, s.errf(e.val, "variable %s must be a string or a mapping", e.name)
		}
		vars = append(vars, v)
	}
	return vars, extras, nil
}

// stageList returns the declared stages, or GitLab's default list when stages is absent.
func (s *state) stageList(root *yaml.Node) ([]string, error) {
	n := get(root, "stages")
	if n == nil {
		return append([]string{}, defaultStages...), nil
	}
	l, ok := stringList(n)
	if !ok {
		return nil, s.errf(n, "stages must be a list of names")
	}
	return l, nil
}
