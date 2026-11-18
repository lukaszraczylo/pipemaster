package gitlab

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// CI_PIPELINE_SOURCE values workflow:rules can map to triggers.
var sourceTriggers = map[string]ir.TriggerKind{
	"merge_request_event": ir.TriggerPR,
	"schedule":            ir.TriggerSchedule,
	"web":                 ir.TriggerManual,
	"push":                ir.TriggerPush,
	"api":                 ir.TriggerAPI,
}

var (
	globLiteralRE = regexp.MustCompile(`^[A-Za-z0-9_\-/]*$`)
	regexEscape   = strings.NewReplacer(`\.`, ".", `\-`, "-", `\/`, "/")
)

// workflow maps workflow:rules to triggers. The block stays in Ext whole; without one, or when a rule has no
// trigger form, the pipeline gets GitLab's default of a push to any branch or tag.
func (s *state) workflow(pl *ir.Pipeline, r *reader) error {
	w := r.peek("workflow")
	trigs, ok, why := s.workflowTriggers(w)
	if w != nil && !ok {
		s.rep.Add(report.Entry{
			IRPath: "pipeline/" + pipelineID, Feature: caps.ExtPassthrough, Level: caps.Lost,
			Strategy: "kept in Ext[gitlab][workflow]; default push and tag triggers used",
			Detail:   "workflow:rules cannot be expressed as triggers: " + why, SourceLoc: s.loc(w),
		})
	}
	if !ok {
		trigs = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
	}
	for _, t := range trigs {
		if t.Kind == ir.TriggerSchedule {
			s.rep.Add(report.Entry{
				IRPath: "pipeline/" + pipelineID, Feature: caps.TriggerSchedule, Level: caps.External,
				Strategy: "configure the schedule in project settings", Detail: "GitLab keeps the cron string outside the CI file; the trigger has no cron", SourceLoc: s.loc(w),
			})
		}
	}
	pl.Triggers = trigs
	return nil
}

func (s *state) workflowTriggers(w *yaml.Node) ([]ir.Trigger, bool, string) {
	if w == nil {
		return nil, false, ""
	}
	rn := get(w, "rules")
	if rn == nil || rn.Kind != yaml.SequenceNode {
		return nil, false, "no rules list"
	}
	var out []ir.Trigger
	for i, it := range rn.Content {
		cond, when, ok := s.workflowRule(it)
		if !ok {
			return nil, false, "a rule uses keys other than if, when, variables or auto_cancel, or an expression the parser cannot model"
		}
		switch {
		case when == whenNever && cond == nil && i == len(rn.Content)-1:
		case when == whenNever:
			return nil, false, "a when: never rule excludes some pipelines"
		case cond == nil:
			out = mergeTriggers(out, []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}})
		default:
			ts, ok := condTriggers(cond)
			if !ok {
				return nil, false, "a condition is not a pipeline source, branch or tag test"
			}
			out = mergeTriggers(out, ts)
		}
	}
	return out, len(out) > 0, "the rules start no pipeline"
}

func (s *state) workflowRule(n *yaml.Node) (cond expr.Expr, when string, ok bool) {
	if n.Kind != yaml.MappingNode {
		return nil, "", false
	}
	when = "always"
	for _, e := range entries(n) {
		switch e.name {
		case "if":
			t, tok := scalarText(e.val)
			if !tok {
				return nil, "", false
			}
			c, err := exprgitlab.Parse(t)
			if err != nil || expr.HasOpaque(c) {
				return nil, "", false
			}
			cond = c
		case "when":
			when, _ = scalarText(e.val)
		case "variables", "auto_cancel":
		default:
			return nil, "", false
		}
	}
	return cond, when, when == "always" || when == whenNever
}

// condTriggers reads a condition built from `||` over source, branch and tag tests.
func condTriggers(e expr.Expr) ([]ir.Trigger, bool) {
	switch v := e.(type) {
	case expr.Binary:
		switch v.Op {
		case expr.OpOr:
			l, lok := condTriggers(v.L)
			r, rok := condTriggers(v.R)
			return mergeTriggers(l, r), lok && rok
		case expr.OpEq, expr.OpMatch:
			return compareTrigger(v)
		}
	case expr.Ref:
		switch v.Path[0] {
		case expr.CtxBranch:
			return []ir.Trigger{{Kind: ir.TriggerPush}}, len(v.Path) == 1
		case expr.CtxTag:
			return []ir.Trigger{{Kind: ir.TriggerTag}}, len(v.Path) == 1
		}
	}
	return nil, false
}

func compareTrigger(b expr.Binary) ([]ir.Trigger, bool) {
	ref, ok := b.L.(expr.Ref)
	lit, lok := b.R.(expr.Lit)
	if !ok || !lok || lit.Kind != expr.LitStr || len(ref.Path) != 1 {
		return nil, false
	}
	pattern := lit.Str
	if b.Op == expr.OpMatch {
		g, gok := regexGlob(pattern)
		if !gok {
			return nil, false
		}
		pattern = g
	}
	switch ref.Path[0] {
	case expr.CtxEvent:
		k, kok := sourceTriggers[lit.Str]
		return []ir.Trigger{{Kind: k}}, kok && b.Op == expr.OpEq
	case expr.CtxBranch:
		return []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{pattern}}}}, true
	case expr.CtxTag:
		return []ir.Trigger{{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{pattern}}}}, true
	}
	return nil, false
}

// regexGlob converts an anchored literal-prefix regexp (^v, ^release-.*$) to a glob; ** stands for ".*".
func regexGlob(p string) (string, bool) {
	body, ok := strings.CutPrefix(p, "^")
	if !ok {
		return "", false
	}
	anchored := strings.HasSuffix(body, "$") && !strings.HasSuffix(body, `\$`)
	body = strings.TrimSuffix(body, "$")
	tail := strings.HasSuffix(body, ".*")
	body = strings.TrimSuffix(body, ".*")
	body = regexEscape.Replace(body)
	if !globLiteralRE.MatchString(body) {
		return "", false
	}
	if tail || !anchored {
		body += "**"
	}
	return body, true
}

// mergeTriggers adds b to a: one trigger per kind, and a filtered trigger widens to "any" when an unfiltered one of its kind is added.
func mergeTriggers(a, b []ir.Trigger) []ir.Trigger {
	for _, t := range b {
		i := indexKind(a, t.Kind)
		if i < 0 {
			a = append(a, t)
			continue
		}
		a[i].Branches = mergeFilter(a[i].Branches, t.Branches)
		a[i].Tags = mergeFilter(a[i].Tags, t.Tags)
	}
	return a
}

func indexKind(ts []ir.Trigger, k ir.TriggerKind) int {
	for i, t := range ts {
		if t.Kind == k {
			return i
		}
	}
	return -1
}

func mergeFilter(a, b ir.Filter) ir.Filter {
	if len(a.Include) == 0 || len(b.Include) == 0 {
		return ir.Filter{}
	}
	out := ir.Filter{Include: append([]string{}, a.Include...)}
	for _, p := range b.Include {
		if !containsStr(out.Include, p) {
			out.Include = append(out.Include, p)
		}
	}
	return out
}
