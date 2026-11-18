package gitlab

import (
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
}

// timing turns Job.If, the status functions and Manual into when and rules. An inexpressible condition
// disables the job, so a translation gap never widens what runs.
func (e *emitter) timing(jp string, j *ir.Job, finally bool) (jobTiming, error) {
	var t jobTiming
	disabled := false
	var cond expr.Expr
	status := ""
	if j.If != nil {
		rest, st, ok := splitStatus(j.If)
		status = st
		switch {
		case !ok:
			e.disable(jp, caps.JobIf, j.If, "job")
			disabled = true
		case rest != nil:
			c, ok := e.condition(jp, caps.JobIf, rest, "job")
			cond, disabled = c, !ok
		}
	}
	base := ""
	switch {
	case j.Manual:
		base = whenManual
	case status == "always" || (finally && status == ""):
		base = whenAlways
	case status == "failure":
		base = whenOnFailure
	}
	if l, ok := cond.(expr.Lit); ok && l.Kind == expr.LitBool {
		if l.Bool {
			cond = nil
		} else {
			disabled = true
		}
	}
	switch {
	case disabled:
		t.rules = seqOf(ruleNever())
	case cond == nil:
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
		r := newMap()
		r.set("if", str(text))
		if base != "" {
			r.set(whenRuleKey, str(base))
		}
		t.rules = seqOf(r.n)
	}
	return t, nil
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
