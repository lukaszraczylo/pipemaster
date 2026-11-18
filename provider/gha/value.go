package gha

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	formatFn      = "format"
	secretsFamily = expr.PrefixSecrets
)

func (fp *fileParser) parts(n *yaml.Node, s string) ([]expr.Part, error) {
	parts, err := gha.ParseTemplate(s)
	if err != nil {
		return nil, fp.errf(n, "%v", err)
	}
	return parts, nil
}

// template reads a scalar as a canonical script template: text plus canonical ${{ }} parts.
func (fp *fileParser) template(n *yaml.Node, what string) (string, error) {
	s, err := fp.scalar(n, what)
	if err != nil {
		return "", err
	}
	return fp.templateString(n, s)
}

func (fp *fileParser) templateString(n *yaml.Node, s string) (string, error) {
	if !strings.Contains(s, "${{") {
		return s, nil
	}
	parts, err := fp.parts(n, s)
	if err != nil {
		return "", err
	}
	out, err := expr.PrintScriptTemplate(parts)
	if err != nil {
		return "", fp.errf(n, "%v", err)
	}
	return out, nil
}

// value turns a scalar into a literal, a secret reference or an expression.
func (fp *fileParser) value(n *yaml.Node, what string) (ir.Value, error) {
	s, err := fp.scalar(n, what)
	if err != nil {
		return ir.Value{}, err
	}
	if !strings.Contains(s, "${{") {
		return ir.Value{Lit: s}, nil
	}
	parts, err := fp.parts(n, s)
	if err != nil {
		return ir.Value{}, err
	}
	if len(parts) == 1 && parts[0].Expr != nil {
		if r, ok := parts[0].Expr.(expr.Ref); ok && len(r.Path) == 2 && r.Path[0] == secretsFamily {
			return ir.Value{Kind: ir.ValueSecret, Secret: r.Path[1]}, nil
		}
	}
	e := mixed(parts)
	if err := expr.Check(e); err != nil {
		return ir.Value{}, fp.errf(n, "%v", err)
	}
	return ir.Value{Kind: ir.ValueExpr, Expr: e}, nil
}

// mixed joins parts into one expression: a lone expression stays itself, text around expressions
// becomes format('…{0}…', args…) with literal braces doubled as GitHub's format() requires.
func mixed(parts []expr.Part) expr.Expr {
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
	return expr.Call{Name: formatFn, Args: args}
}

// cond reads an if: value, wrapped in ${{ }} or bare.
func (fp *fileParser) cond(n *yaml.Node) (expr.Expr, error) {
	s, err := fp.scalar(n, "if")
	if err != nil {
		return nil, err
	}
	var e expr.Expr
	if strings.Contains(s, "${{") {
		parts, perr := fp.parts(n, s)
		if perr != nil {
			return nil, perr
		}
		e = mixed(trimBlank(parts))
	} else if e, err = gha.Parse(s); err != nil {
		return nil, fp.errf(n, "if: %v", err)
	}
	if err := expr.Check(e); err != nil {
		return nil, fp.errf(n, "if: %v", err)
	}
	return e, nil
}

// trimBlank drops whitespace-only text around a lone expression so `if: ${{ x }} ` reads as x.
func trimBlank(parts []expr.Part) []expr.Part {
	var out []expr.Part
	exprs := 0
	for _, p := range parts {
		if p.Expr != nil {
			exprs++
		}
	}
	for _, p := range parts {
		if exprs == 1 && p.Expr == nil && strings.TrimSpace(p.Text) == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// vars reads an env mapping in source order; ok is false when env is not a mapping (an expression).
func (fp *fileParser) vars(n *yaml.Node, what string) (vs []ir.Var, ok bool, err error) {
	n = deref(n)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, false, nil
	}
	m, err := fp.mapping(n, what)
	if err != nil {
		return nil, false, err
	}
	for _, e := range m.entries {
		v, err := fp.value(e.val, what+"."+e.key)
		if err != nil {
			return nil, false, err
		}
		vs = append(vs, ir.Var{Name: e.key, Value: v})
	}
	return vs, true, nil
}

// with reads an action's inputs.
func (fp *fileParser) with(n *yaml.Node) (map[string]ir.Value, error) {
	m, err := fp.mapping(n, "with")
	if err != nil {
		return nil, err
	}
	out := make(map[string]ir.Value, len(m.entries))
	for _, e := range m.entries {
		v, err := fp.value(e.val, "with."+e.key)
		if err != nil {
			return nil, err
		}
		out[e.key] = v
	}
	return out, nil
}
