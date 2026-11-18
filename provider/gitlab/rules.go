package gitlab

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

type ruleResult struct {
	cond   expr.Expr
	manual bool
}

// rule is one parsed `if`/`when` pair; cond is nil when the rule has no `if` and so matches everything.
type rule struct {
	cond expr.Expr
	when string
}

// compileRules turns an if-only rules list into one condition with GitLab's first-match meaning:
// a rule applies only when no earlier rule matched. ok is false for anything that has no such form.
func (s *state) compileRules(n *yaml.Node, defaultWhen string) (ruleResult, bool) {
	if n.Kind != yaml.SequenceNode {
		return ruleResult{}, false
	}
	var rs []rule
	for _, it := range n.Content {
		r, ok := s.compileRule(it, defaultWhen)
		if !ok {
			return ruleResult{}, false
		}
		rs = append(rs, r)
	}
	var res ruleResult
	var terms []expr.Expr
	var earlier []expr.Expr
	mode := ""
	matchesAll := false
	for _, r := range rs {
		if r.when != whenNever {
			if mode != "" && mode != r.when {
				return ruleResult{}, false
			}
			mode = r.when
			terms = append(terms, andNotAll(r.cond, earlier))
			if r.cond == nil && len(earlier) == 0 {
				matchesAll = true
			}
		}
		if r.cond == nil {
			break
		}
		earlier = append(earlier, r.cond)
	}
	res.manual = mode == whenManual
	switch {
	case len(terms) == 0:
		res.cond = expr.Lit{Kind: expr.LitBool, Bool: false}
	case matchesAll:
		res.cond = nil
	default:
		res.cond = orAll(terms)
	}
	if res.cond != nil && expr.Check(res.cond) != nil {
		return ruleResult{}, false
	}
	return res, true
}

func (s *state) compileRule(n *yaml.Node, defaultWhen string) (rule, bool) {
	r := rule{when: defaultWhen}
	if n.Kind != yaml.MappingNode {
		return r, false
	}
	for _, e := range entries(n) {
		switch e.name {
		case "if":
			t, ok := scalarText(e.val)
			if !ok {
				return r, false
			}
			c, err := exprgitlab.Parse(t)
			if err != nil || expr.HasOpaque(c) {
				return r, false
			}
			r.cond = c
		case "when":
			t, ok := scalarText(e.val)
			if !ok {
				return r, false
			}
			r.when = t
		default:
			return r, false
		}
	}
	switch r.when {
	case whenOnSuccess, whenNever, whenManual:
		return r, true
	}
	return r, false
}

// andNotAll is cond && !e1 && !e2 ...; a nil cond is true.
func andNotAll(cond expr.Expr, earlier []expr.Expr) expr.Expr {
	out := cond
	for _, e := range earlier {
		out = and(out, expr.Unary{Op: expr.OpNot, X: e})
	}
	return out
}

func orAll(terms []expr.Expr) expr.Expr {
	out := terms[0]
	for _, t := range terms[1:] {
		out = expr.Binary{Op: expr.OpOr, L: out, R: t}
	}
	return out
}
