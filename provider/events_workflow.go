package provider

import (
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// Admission says whether the workflow rules of a GitLab pipeline start a pipeline from a source.
type Admission int

const (
	// AdmitNo means the rules never start a pipeline from the source.
	AdmitNo Admission = iota
	// AdmitYes means the rules start a pipeline from the source whatever the branch, the tag or the variables are.
	AdmitYes
	// AdmitMaybe means the start depends on a test that is not a source test, or on a rule that does not compile.
	AdmitMaybe
)

const (
	ruleIf    = "if"
	ruleWhen  = "when"
	whenNever = "never"
)

// WorkflowAdmits reads the workflow:rules block that the GitLab parser keeps in Ext, in list order: the first rule
// that matches decides, and a pipeline that matches no rule does not start. A pipeline with no block admits every source.
// For AdmitMaybe the second result is the condition on the branch, the tag and the variables under which the rules start
// a pipeline from the source, and nil when a rule does not compile.
func WorkflowAdmits(pl *ir.Pipeline, source string) (Admission, expr.Expr) {
	m, _ := pl.Ext[GitLab].(map[string]any)
	wf, has := m["workflow"]
	if !has {
		return AdmitYes, nil
	}
	block, _ := wf.(map[string]any)
	rules, ok := block["rules"].([]any)
	if !ok {
		return AdmitMaybe, nil
	}
	var admitted expr.Expr = expr.Lit{Kind: expr.LitBool}
	var earlier []expr.Expr
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			return AdmitMaybe, nil
		}
		cond, ok := ruleCond(rule)
		if !ok {
			return AdmitMaybe, nil
		}
		var c expr.Expr = expr.Lit{Kind: expr.LitBool, Bool: true}
		if cond != nil {
			c = expr.FoldEvent(cond, source)
		}
		if rule[ruleWhen] != whenNever {
			term := c
			for _, prev := range earlier {
				term = expr.Binary{Op: expr.OpAnd, L: term, R: expr.Unary{Op: expr.OpNot, X: prev}}
			}
			admitted = expr.Binary{Op: expr.OpOr, L: admitted, R: term}
		}
		earlier = append(earlier, c)
		if cond == nil {
			break
		}
	}
	admitted = expr.FoldConst(admitted)
	if lit, isLit := admitted.(expr.Lit); isLit && lit.Kind == expr.LitBool {
		if lit.Bool {
			return AdmitYes, nil
		}
		return AdmitNo, nil
	}
	return AdmitMaybe, admitted
}

// ruleCond compiles the if key of a workflow rule. A rule with no if key matches every pipeline and gives a nil condition.
func ruleCond(rule map[string]any) (expr.Expr, bool) {
	for k := range rule {
		if k != ruleIf && k != ruleWhen && k != "variables" && k != "auto_cancel" {
			return nil, false
		}
	}
	text, has := rule[ruleIf]
	if !has {
		return nil, true
	}
	s, ok := text.(string)
	if !ok {
		return nil, false
	}
	c, err := exprgitlab.Parse(s)
	if err != nil || expr.HasOpaque(c) {
		return nil, false
	}
	return c, true
}
