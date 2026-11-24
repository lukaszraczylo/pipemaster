package gitlab

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

type ruleResult struct {
	cond expr.Expr
	// manual is true when every arm that can match starts the job by hand.
	manual bool
	// dropped is true when a list mixed automatic and manual arms: the manual arms are left out.
	dropped bool
}

// rule is one parsed rule. cond is nil when the rule has no `if` and no `changes`, so it matches everything.
type rule struct {
	cond         expr.Expr
	allowFailure *bool
	when         string
}

// ruleKeys are the keys of a job rule that compile. Anything else (exists, variables, needs, interruptible)
// changes the job in a way the IR cannot hold, so the whole list stays opaque.
const (
	keyIf           = "if"
	keyWhen         = "when"
	keyChanges      = "changes"
	keyAllowFailure = "allow_failure"
	keyPaths        = "paths"
)

// compileRules turns a rules list into one condition with GitLab's first-match meaning: a rule applies only
// when no earlier rule matched. ok is false for anything that has no such form. allowFailure is the job's own
// setting; a rule that sets another value cannot compile, because the IR holds one value per job.
func (s *state) compileRules(n *yaml.Node, defaultWhen string, allowFailure bool) (ruleResult, bool) {
	if n.Kind != yaml.SequenceNode {
		return ruleResult{}, false
	}
	var rs []rule
	for _, it := range n.Content {
		r, ok := s.compileRule(it, defaultWhen, false)
		if !ok {
			return ruleResult{}, false
		}
		rs = append(rs, r)
	}
	return foldRuleList(rs, allowFailure)
}

// workflowCond compiles workflow:rules, where when is always or never and the variables and auto_cancel keys
// do not decide whether the pipeline exists.
func (s *state) workflowCond(w *yaml.Node) (expr.Expr, bool) {
	rn := get(w, "rules")
	if rn == nil || rn.Kind != yaml.SequenceNode {
		return nil, false
	}
	var rs []rule
	for _, it := range rn.Content {
		r, ok := s.compileRule(it, whenOnSuccess, true)
		if !ok {
			return nil, false
		}
		rs = append(rs, r)
	}
	res, ok := foldRuleList(rs, false)
	return res.cond, ok
}

// foldRuleList joins the rules in list order. A manual arm that sits beside automatic arms is dropped and reported
// by the caller: the job never starts by itself on a branch of the condition that GitLab starts by hand.
func foldRuleList(rs []rule, allowFailure bool) (ruleResult, bool) {
	var auto, manual, earlier []expr.Expr
	var autoAF, manualAF []*bool
	matchesAll := false
	for _, r := range rs {
		if r.when != whenNever {
			term := andNotAll(r.cond, earlier)
			if r.when == whenManual {
				manual, manualAF = append(manual, term), append(manualAF, r.allowFailure)
			} else {
				auto, autoAF = append(auto, term), append(autoAF, r.allowFailure)
			}
			if r.cond == nil && len(earlier) == 0 {
				matchesAll = true
			}
		}
		if r.cond == nil {
			break
		}
		earlier = append(earlier, r.cond)
	}
	var res ruleResult
	terms, kept := auto, autoAF
	if len(auto) == 0 {
		terms, kept, res.manual = manual, manualAF, len(manual) > 0
	} else if len(manual) > 0 {
		res.dropped = true
	}
	for _, af := range kept {
		if af != nil && *af != allowFailure {
			return ruleResult{}, false
		}
	}
	switch {
	case len(terms) == 0:
		res.cond = expr.Lit{Kind: expr.LitBool, Bool: false}
	case matchesAll && !res.dropped:
		res.cond = nil
	default:
		res.cond = orAll(terms)
	}
	if res.cond != nil && expr.Check(res.cond) != nil {
		return ruleResult{}, false
	}
	return res, true
}

func (s *state) compileRule(n *yaml.Node, defaultWhen string, workflow bool) (rule, bool) {
	r := rule{when: defaultWhen}
	if n.Kind != yaml.MappingNode {
		return r, false
	}
	var ifCond, paths expr.Expr
	for _, e := range entries(n) {
		switch e.name {
		case keyIf:
			t, ok := scalarText(e.val)
			if !ok {
				return r, false
			}
			c, err := exprgitlab.Parse(t)
			if err != nil || expr.HasOpaque(c) {
				return r, false
			}
			ifCond = c
		case keyWhen:
			t, ok := scalarText(e.val)
			if !ok {
				return r, false
			}
			if workflow && t == whenAlways {
				t = whenOnSuccess
			}
			r.when = t
		case keyChanges:
			globs, ok := changesGlobs(e.val)
			if !ok || workflow {
				return r, false
			}
			paths = expr.Changes(globs...)
		case keyAllowFailure:
			if workflow {
				return r, false
			}
			b, ok := boolScalar(e.val)
			if !ok {
				return r, false
			}
			r.allowFailure = &b
		case "variables", "auto_cancel":
			if !workflow {
				return r, false
			}
		default:
			return r, false
		}
	}
	r.cond = and(ifCond, paths)
	switch r.when {
	case whenOnSuccess, whenNever:
		return r, true
	case whenManual:
		return r, !workflow
	}
	return r, false
}

func boolScalar(n *yaml.Node) (bool, bool) {
	t, ok := scalarText(n)
	switch t {
	case "true":
		return true, ok
	case "false":
		return false, ok
	}
	return false, false
}

// changesGlobs reads a rules:changes value: a list of paths, or a mapping with only paths. A compare_to ref
// changes what is compared, a variable in a path is not known here, and a regexp is not a glob, so none compile.
func changesGlobs(n *yaml.Node) ([]string, bool) {
	if n.Kind == yaml.MappingNode {
		es := entries(n)
		if len(es) != 1 || es[0].name != keyPaths {
			return nil, false
		}
		n = es[0].val
	}
	if n.Kind != yaml.SequenceNode || len(n.Content) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(n.Content))
	for _, it := range n.Content {
		g, ok := scalarText(it)
		if !ok || !okGlob(g) {
			return nil, false
		}
		out = append(out, g)
	}
	return out, true
}

// okGlob admits the glob syntax of Ruby's File.fnmatch that the target emitters spell out: literal characters,
// *, **, ?, [] classes and {a,b} lists. A variable, a backslash escape or a relative path has no such reading.
func okGlob(g string) bool {
	if g == "" || strings.ContainsAny(g, "$\\\n\x00'\"`") || strings.HasPrefix(g, "./") || strings.HasPrefix(g, "/") || strings.Contains(g, "//") {
		return false
	}
	return true
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
