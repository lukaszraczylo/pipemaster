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
func WorkflowAdmits(pl *ir.Pipeline, source string) Admission {
	m, _ := pl.Ext[GitLab].(map[string]any)
	wf, has := m["workflow"]
	if !has {
		return AdmitYes
	}
	block, _ := wf.(map[string]any)
	rules, ok := block["rules"].([]any)
	if !ok {
		return AdmitMaybe
	}
	src := []string{source}
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			return AdmitMaybe
		}
		cond, ok := ruleCond(rule)
		if !ok {
			return AdmitMaybe
		}
		certain := true
		if cond != nil {
			if len(expr.EventsPossible(cond, src)) == 0 {
				continue
			}
			certain = len(expr.EventsPossible(expr.Unary{Op: expr.OpNot, X: cond}, src)) == 0
		}
		switch {
		case !certain:
			return AdmitMaybe
		case rule[ruleWhen] == whenNever:
			return AdmitNo
		}
		return AdmitYes
	}
	return AdmitNo
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
