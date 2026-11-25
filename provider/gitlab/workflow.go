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
// "push" also covers tag pushes: GitLab runs a tag pipeline with source push.
var sourceTriggers = map[string][]ir.TriggerKind{
	"merge_request_event": {ir.TriggerPR},
	"schedule":            {ir.TriggerSchedule},
	"web":                 {ir.TriggerManual},
	"push":                {ir.TriggerPush, ir.TriggerTag},
	"api":                 {ir.TriggerAPI},
}

var (
	globLiteralRE = regexp.MustCompile(`^[A-Za-z0-9_\-/]*$`)
	regexEscape   = strings.NewReplacer(`\.`, ".", `\-`, "-", `\/`, "/")
)

// workflow maps workflow:rules to triggers. The block stays in Ext whole; without one, or when a rule has no
// trigger form, the pipeline gets GitLab's default of a push to any branch or tag, and the returned guard
// is an opaque condition for every job, because the default triggers are wider than the rules.
func (s *state) workflow(pl *ir.Pipeline, r *reader) (guard expr.Expr, err error) {
	w := r.peek("workflow")
	var named bool
	trigs, ok, why := s.workflowTriggers(w)
	if w != nil {
		if c, cok := s.workflowCond(w); cok {
			s.wfCond = c
		}
	}
	if w != nil && !ok {
		guard, err = s.workflowGuard(w)
		if err != nil {
			return nil, err
		}
		detail := "workflow:rules cannot be expressed as triggers: " + why
		if _, opaque := guard.(expr.Opaque); !opaque {
			detail += "; the rules are ANDed into every job condition"
		}
		s.rep.Add(report.Entry{
			IRPath: "pipeline/" + pipelineID, Feature: caps.ExtPassthrough, Level: caps.Lost,
			Strategy: "kept in Ext[gitlab][workflow]; default push and tag triggers used",
			Detail:   detail, SourceLoc: s.loc(w),
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
	if n := get(w, "name"); n != nil {
		if t, tok := scalarText(n); tok && pipelineName(t) {
			pl.Name = t
			named = true
		}
	}
	s.noteWorkflowKeys(w, named)
	pl.Triggers = trigs
	return guard, nil
}

// workflowGuard is the condition every job gets when the workflow rules have no trigger form: their canonical
// condition, or the block as an opaque condition when it does not compile. A nil condition admits every pipeline.
func (s *state) workflowGuard(w *yaml.Node) (expr.Expr, error) {
	if s.wfCond != nil {
		return s.wfCond, nil
	}
	if c, ok := s.workflowCond(w); ok {
		return c, nil
	}
	v, err := s.plain(w)
	if err != nil {
		return nil, err
	}
	b, err := yaml.Marshal(map[string]any{"workflow": v})
	if err != nil {
		return nil, s.errf(w, "%v", err)
	}
	return expr.Opaque{Dialect: dialect, Text: string(b)}, nil
}

func (s *state) workflowTriggers(w *yaml.Node) ([]ir.Trigger, bool, string) {
	if w == nil {
		return nil, false, ""
	}
	rn := get(w, "rules")
	if rn == nil && len(entries(w)) == 1 && get(w, "name") != nil {
		return []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}, true, ""
	}
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
	return out, true, ""
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
		case expr.OpAnd:
			if isPushSource(v.L) {
				return condTriggers(v.R)
			}
		}
	case expr.Ref:
		if strings.Join(v.Path, ".") == expr.CtxPRNumber {
			return []ir.Trigger{{Kind: ir.TriggerPR}}, true
		}
		switch v.Path[0] {
		case expr.CtxBranch:
			return []ir.Trigger{{Kind: ir.TriggerPush}}, len(v.Path) == 1
		case expr.CtxTag:
			return []ir.Trigger{{Kind: ir.TriggerTag}}, len(v.Path) == 1
		}
	}
	return nil, false
}

// isPushSource reports the test that scopes a branch or tag rule to a push (triggerRule, scoped).
func isPushSource(e expr.Expr) bool {
	b, ok := e.(expr.Binary)
	if !ok || b.Op != expr.OpEq {
		return false
	}
	ref, rok := b.L.(expr.Ref)
	lit, lok := b.R.(expr.Lit)
	return rok && lok && len(ref.Path) == 1 && ref.Path[0] == expr.CtxEvent && lit.Kind == expr.LitStr && lit.Str == sourcePush
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
		kinds, kok := sourceTriggers[lit.Str]
		var ts []ir.Trigger
		for _, k := range kinds {
			ts = append(ts, ir.Trigger{Kind: k})
		}
		return ts, kok && b.Op == expr.OpEq
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

// guardJobs ANDs the opaque workflow condition into every job unless trigger markers restored the triggers, because
// the default triggers are wider than the rules the file declares.
func (s *state) guardJobs(pl *ir.Pipeline) {
	if s.guard == nil || s.restored {
		return
	}
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		j.If = and(j.If, s.guard)
	}
}

// noteWorkflowKeys reports the workflow settings that change a pipeline once it starts: its name, auto-cancel
// policy and the variables a rule sets. The IR has no field for them, so they stay in Ext.
func (s *state) noteWorkflowKeys(w *yaml.Node, nameModelled bool) {
	path := "pipeline/" + pipelineID
	for _, e := range entries(w) {
		switch e.name {
		case "name", "auto_cancel":
			if e.name == "name" && nameModelled {
				continue
			}
			s.note(path, caps.ExtPassthrough, "kept in Ext[gitlab]", "workflow:"+e.name+" has no IR field; kept in Ext and not written to other targets", e.key)
		case "rules":
			for _, r := range e.val.Content {
				for _, k := range entries(r) {
					if k.name == "variables" || k.name == "auto_cancel" {
						s.note(path, caps.ExtPassthrough, "kept in Ext[gitlab]", "workflow:rules "+k.name+" has no IR field; kept in Ext and not written to other targets", k.key)
					}
				}
			}
		}
	}
}
