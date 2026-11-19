package gitlab

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
)

// jobWhenRules maps `when` and `rules`. when: always and on_failure move the job to Finally;
// rules that fit one condition become Job.If, anything else stays as an Opaque gitlab expression.
func (s *state) jobWhenRules(info *jobInfo, r *reader) error {
	j := info.job
	when := whenOnSuccess
	delayed := false
	if n := r.take("when"); n != nil {
		when, _ = scalarText(n)
		delayed = when == whenDelayed
		switch when {
		case whenOnSuccess, whenManual, whenAlways, whenOnFailure, whenNever:
		default:
			r.release("when")
			s.note(s.jobPath(j.ID), caps.JobManual, "kept in Ext[gitlab]", "when: "+when+" has no IR form", n)
			when = whenOnSuccess
		}
	}
	rulesNode := r.take("rules")
	var cond expr.Expr
	manualFromRules := false
	if rulesNode != nil {
		res, ok := s.compileRules(rulesNode, when)
		if ok {
			cond, manualFromRules = res.cond, res.manual
			if cond == nil && !manualFromRules {
				// Rules that always match still replace the rules of an extended job.
				r.keep(ExtRulesAlways, true)
			}
			// The keyword when only supplies the rules' default, already applied by compileRules.
			when = whenOnSuccess
		} else {
			r.release("rules")
			op, err := s.opaqueRules(rulesNode)
			if err != nil {
				return err
			}
			cond = op
			s.note(s.jobPath(j.ID), caps.ExprOpaque, "kept as an opaque gitlab expression and in Ext[gitlab][rules]", "rules use features that have no canonical condition (changes, exists, variables, needs, allow_failure, when other than manual) or an expression the parser cannot model", rulesNode)
		}
	}
	native, err := s.nativeConditions(r, delayed)
	if err != nil {
		return err
	}
	switch when {
	case whenManual:
		j.Manual = true
	case whenAlways:
		info.finally = true
	case whenOnFailure:
		info.finally = true
		cond = and(cond, expr.Call{Name: "failure"})
	case whenNever:
		cond = expr.Lit{Kind: expr.LitBool, Bool: false}
		s.note(s.jobPath(j.ID), caps.JobIf, "job kept with If false", "when: never means the job never runs", rulesNode)
	}
	if manualFromRules {
		j.Manual = true
	}
	if native != nil {
		cond = and(cond, native)
	}
	j.If = cond
	if j.Manual && rulesNode == nil && r.peek("allow_failure") == nil {
		j.AllowFailure = true
	}
	return nil
}

func (s *state) opaqueRules(n *yaml.Node) (expr.Expr, error) {
	v, err := s.plain(n)
	if err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, s.errf(n, "%v", err)
	}
	return expr.Opaque{Dialect: dialect, Text: string(b)}, nil
}

// and joins two optional conditions; nil means "always true".
func and(a, b expr.Expr) expr.Expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return expr.Binary{Op: expr.OpAnd, L: a, R: b}
}

// nativeConditions keeps the GitLab-only keys that decide when a job runs (only, except, when: delayed) as one
// opaque condition, so a target that cannot state them disables the job. The keys stay in Ext for the emitter.
func (s *state) nativeConditions(r *reader, delayed bool) (expr.Expr, error) {
	keys := map[string]any{}
	if delayed {
		keys["when"] = whenDelayed
		if n := r.peek("start_in"); n != nil {
			v, err := s.plain(n)
			if err != nil {
				return nil, err
			}
			keys["start_in"] = v
		}
	}
	for _, k := range []string{"only", "except"} {
		if n := r.peek(k); n != nil {
			v, err := s.plain(n)
			if err != nil {
				return nil, err
			}
			keys[k] = v
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	b, err := yaml.Marshal(keys)
	if err != nil {
		return nil, err
	}
	return expr.Opaque{Dialect: dialect, Text: string(b)}, nil
}

// isNativeCondition is true for an opaque gitlab condition that is a key map (only, except, when, workflow), as
// opposed to a rules list.
func isNativeCondition(x expr.Expr) bool {
	o, ok := x.(expr.Opaque)
	if !ok || o.Dialect != dialect {
		return false
	}
	var doc yaml.Node
	if yaml.Unmarshal([]byte(o.Text), &doc) != nil || len(doc.Content) != 1 {
		return false
	}
	return doc.Content[0].Kind == yaml.MappingNode
}

// withoutNative drops the native key-map conditions from a conjunction; the emitter writes those keys from Ext.
func withoutNative(x expr.Expr) expr.Expr {
	if isNativeCondition(x) {
		return nil
	}
	if b, ok := x.(expr.Binary); ok && b.Op == expr.OpAnd {
		return and(withoutNative(b.L), withoutNative(b.R))
	}
	return x
}
