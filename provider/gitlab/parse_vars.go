package gitlab

import (
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// variables reads a variables mapping; extras holds the description, options and expand keys of the hash form by name.
func (s *state) variables(path string, n *yaml.Node) (vars []ir.Var, extras map[string]any, err error) {
	if n.Kind != yaml.MappingNode {
		return nil, nil, s.errf(n, "variables must be a mapping")
	}
	for _, e := range entries(n) {
		v := ir.Var{Name: e.name}
		switch e.val.Kind {
		case yaml.ScalarNode:
			t, _ := scalarText(e.val)
			v.Value = s.varValue(path, e.name, t, e.val)
		case yaml.MappingNode:
			r, err := s.reader(e.val, "variable "+e.name)
			if err != nil {
				return nil, nil, err
			}
			if val := r.take("value"); val != nil {
				t, err := s.scalar(val, "variable "+e.name+" value")
				if err != nil {
					return nil, nil, err
				}
				v.Value = s.varValue(path, e.name, t, val)
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

// varValue canonicalises the predefined variables in a variable's value: one reference is a Ref, text around
// references is a format() call, and any other $NAME stays text. GitLab expands those on the runner, so a
// report note warns that other targets may not.
func (s *state) varValue(path, name, text string, n *yaml.Node) ir.Value {
	for _, u := range unknownVars(text) {
		s.note(path+"/env/"+name, caps.ScriptInterpolation, "kept as GitLab text", "value references $"+u+", which GitLab expands; other targets may not", n)
	}
	parts := splitVars(text)
	if !slices.ContainsFunc(parts, func(p expr.Part) bool { return p.Expr != nil }) {
		return ir.Value{Lit: text}
	}
	return ir.Value{Kind: ir.ValueExpr, Expr: mixFormat(parts)}
}

// unknownVars lists the $NAME and ${NAME} references that are not predefined variables the vocabulary maps.
func unknownVars(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			i++
			continue
		}
		name, end := varAt(s, i)
		if name == "" {
			continue
		}
		if _, ok := exprgitlab.Vocab.Canon([]string{name}); !ok && !slices.Contains(out, name) {
			out = append(out, name)
		}
		i = end - 1
	}
	return out
}

// mixFormat joins parts into one expression, as the GitHub Actions parser does: a lone reference stays itself,
// text around references becomes format('..{0}..', args) with literal braces doubled.
func mixFormat(parts []expr.Part) expr.Expr {
	if len(parts) == 1 && parts[0].Expr != nil {
		return parts[0].Expr
	}
	var pat strings.Builder
	args := []expr.Expr{nil}
	for _, p := range parts {
		if p.Expr == nil {
			pat.WriteString(strings.NewReplacer("{", "{{", "}", "}}").Replace(p.Text))
			continue
		}
		pat.WriteString("{" + strconv.Itoa(len(args)-1) + "}")
		args = append(args, p.Expr)
	}
	args[0] = expr.Lit{Kind: expr.LitStr, Str: pat.String()}
	return expr.Call{Name: "format", Args: args}
}
