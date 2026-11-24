package gitlab

import (
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const whenRuleKey = "when"

// jobTiming is how a job's condition, status and manual flag are written.
type jobTiming struct {
	rules *yaml.Node
	when  string
	marks []string
}

// timing turns Job.If, the status functions and Manual into when and rules. An inexpressible condition
// disables the job, so a translation gap never widens what runs.
func (e *emitter) timing(jp string, j *ir.Job, finally bool) (jobTiming, error) {
	var t jobTiming
	if n, ok := e.keptRules(j, finally); ok {
		t.rules = n
		t.when, _ = extOf(j.Ext)[ExtRulesWhen].(string)
		return t, nil
	}
	disabled := false
	var cond expr.Expr
	status := ""
	var srcIf, translated expr.Expr
	if ifx := e.withoutGuard(withoutNative(j.If)); ifx != nil {
		rest, st, ok := splitStatus(ifx)
		status = st
		switch {
		case !ok:
			e.disable(jp, caps.JobIf, j.If, "job")
			disabled = true
		case rest != nil:
			c, ok := e.condition(jp, caps.JobIf, rest, "job")
			cond, disabled = c, !ok
			if ok && e.from != nil && st == "" {
				srcIf, translated = rest, c
			}
		}
	}
	base := ""
	switch {
	case j.Manual:
		base = whenManual
		if finally {
			e.lose(jp, caps.PipelineFinally, "a finally job is manual, and GitLab has one when key: the job starts by hand and no longer runs after the other jobs")
		}
	case status == "always" || (finally && status == ""):
		base = whenAlways
	case status == "failure":
		base = whenOnFailure
	}
	fj := e.foldJob(j)
	if fj.disabled {
		e.lose(jp, caps.JobIf, "pipeline "+fj.pid+" has no trigger GitLab can express; job disabled")
		disabled = true
	}
	if fj.cond != nil || len(fj.parts) > 0 {
		if _, opaque := cond.(expr.Opaque); opaque {
			e.lose(jp, caps.ExprOpaque, "the job's opaque gitlab rules no longer include its pipeline's trigger condition or path filter, so the job runs on every trigger of the file")
		} else if fj.cond != nil {
			cond = andAll(fj.cond, cond)
		}
	}
	if l, ok := cond.(expr.Lit); ok && l.Kind == expr.LitBool {
		if l.Bool {
			cond = nil
		} else {
			disabled = true
		}
	}
	_, opaqueCond := cond.(expr.Opaque)
	switch {
	case disabled:
		if j.Manual {
			e.lose(jp, caps.JobManual, "a job that never runs has no manual start")
		}
		t.rules = seqOf(ruleNever())
	case len(fj.parts) > 0 && !opaqueCond:
		rules, err := foldRules(fj.parts, cond, base)
		if err != nil {
			return t, err
		}
		t.rules = rules
	case cond == nil:
		if always, _ := extOf(j.Ext)[ExtRulesAlways].(bool); always && base == "" {
			r := newMap()
			r.set(whenRuleKey, str(whenOnSuccess))
			t.rules = seqOf(r.n)
			break
		}
		t.when = base
	default:
		if o, ok := cond.(expr.Opaque); ok {
			n, err := decodeRules(o.Text)
			if err != nil {
				e.lose(jp, caps.ExprOpaque, "opaque gitlab rules are not a YAML list: "+err.Error())
				t.rules = seqOf(ruleNever())
				break
			}
			t.rules, t.when = n, base
			break
		}
		text, err := exprgitlab.Print(cond)
		if err != nil {
			return t, err
		}
		if srcIf != nil && expr.String(cond) == expr.String(translated) && expr.String(srcIf) != expr.String(translated) {
			if t.marks, err = e.srcMarks(jp, srcIf); err != nil {
				return t, err
			}
		}
		r := newMap()
		r.set("if", str(text))
		if base != "" {
			r.set(whenRuleKey, str(base))
		}
		t.rules = seqOf(r.n)
	}
	return t, nil
}

// keptRules is the rules text of a job that still has the condition and manual flag it compiled to. The text is
// then the exact source, with the changes lists and the manual arms the condition cannot print.
func (e *emitter) keptRules(j *ir.Job, finally bool) (*yaml.Node, bool) {
	ext := extOf(j.Ext)
	fp, _ := ext[ExtRulesFP].(string)
	raw := ext["rules"]
	if finally || fp == "" || raw == nil {
		return nil, false
	}
	if fj := e.foldJob(j); fj.disabled || fj.cond != nil || len(fj.parts) > 0 {
		return nil, false
	}
	if rulesFP(e.withoutGuard(withoutNative(j.If)), j.Manual) != fp {
		return nil, false
	}
	n, err := plainNode(raw)
	if err != nil || n.Kind != yaml.SequenceNode {
		return nil, false
	}
	return n, true
}

// withoutGuard drops the workflow condition that Parse ANDed into the job: the workflow block is written back whole.
func (e *emitter) withoutGuard(x expr.Expr) expr.Expr {
	if e.wfGuard == nil || x == nil {
		return x
	}
	g := expr.String(e.wfGuard)
	if expr.String(x) == g {
		return nil
	}
	if b, ok := x.(expr.Binary); ok && b.Op == expr.OpAnd && expr.String(b.R) == g {
		return b.L
	}
	return x
}

func ruleNever() *yaml.Node {
	r := newMap()
	r.set(whenRuleKey, str(whenNever))
	return r.n
}

func decodeRules(text string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.SequenceNode {
		return nil, errNotRulesList
	}
	return doc.Content[0], nil
}

// foldRules writes one rule per trigger, each with the job's own condition and the trigger's path filter.
func foldRules(parts []foldPart, cond expr.Expr, base string) (*yaml.Node, error) {
	rules := seqOf()
	for _, p := range parts {
		text, err := exprgitlab.Print(andAll(p.cond, cond))
		if err != nil {
			return nil, err
		}
		r := newMap()
		r.set("if", str(text))
		if len(p.changes) > 0 {
			r.set("changes", strSeq(p.changes))
		}
		if base != "" {
			r.set(whenRuleKey, str(base))
		}
		rules.Content = append(rules.Content, r.n)
	}
	return rules, nil
}

// srcMarks records the job condition in the source dialect, which the translated rule only approximates.
func (e *emitter) srcMarks(jp string, src expr.Expr) ([]string, error) {
	a, err := markerArg(srcArg{Src: expr.String(src), From: e.from.ID})
	if err != nil {
		return nil, err
	}
	return []string{emitkit.Marker(caps.JobIf, jp), a}, nil
}
