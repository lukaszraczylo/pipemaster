package gitlab

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	scheduleInstruction = "create a pipeline schedule: Settings > CI/CD > Schedules, cron `%s`"
	apiInstruction      = "create a pipeline trigger token (Settings > CI/CD > Pipeline trigger tokens) or call the pipelines API"
	sourceMR            = "merge_request_event"
	globChars           = "*?["
)

var globLiteral = regexp.MustCompile(`^[A-Za-z0-9_\-/]*$`)

func triggerPath(pid string, i int) string { return "pipeline/" + pid + "/trigger/" + strconv.Itoa(i) }

func ref1(name string) expr.Expr { return expr.Ref{Path: []string{name}} }

func strLit(s string) expr.Expr { return expr.Lit{Kind: expr.LitStr, Str: s} }

func binary(op string, l, r expr.Expr) expr.Expr { return expr.Binary{Op: op, L: l, R: r} }

func andAll(xs ...expr.Expr) expr.Expr {
	var out expr.Expr
	for _, x := range xs {
		if x == nil {
			continue
		}
		if out == nil {
			out = x
		} else {
			out = binary(expr.OpAnd, out, x)
		}
	}
	return out
}

func orAny(xs []expr.Expr) expr.Expr {
	var out expr.Expr
	for _, x := range xs {
		if out == nil {
			out = x
		} else {
			out = binary(expr.OpOr, out, x)
		}
	}
	return out
}

// globRegex turns a trigger glob into an anchored regexp. A literal plus a trailing "**" becomes ^literal.*$,
// which the parser reads back as the same glob.
func globRegex(p string) string {
	if body, ok := strings.CutSuffix(p, "**"); ok && globLiteral.MatchString(body) {
		return "^" + regexp.QuoteMeta(body) + ".*$"
	}
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			sb.WriteString(".*")
			i++
		case p[i] == '*':
			sb.WriteString("[^/]*")
		case p[i] == '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(p[i])))
		}
	}
	return sb.String() + "$"
}

// patternTest is v == "glob" for a literal glob and v =~ /regex/ otherwise.
func patternTest(v expr.Expr, glob string, negate bool) expr.Expr {
	if !strings.ContainsAny(glob, globChars) && !strings.Contains(glob, "**") {
		op := expr.OpEq
		if negate {
			op = expr.OpNe
		}
		return binary(op, v, strLit(glob))
	}
	op := expr.OpMatch
	if negate {
		op = expr.OpNoMatch
	}
	return binary(op, v, strLit(globRegex(glob)))
}

// filterCond is "any include matches and no exclude matches" on v; with no include the ref itself must be set.
func filterCond(v expr.Expr, f ir.Filter) expr.Expr {
	var inc []expr.Expr
	for _, p := range f.Include {
		inc = append(inc, patternTest(v, p, false))
	}
	parts := []expr.Expr{orAny(inc)}
	if len(inc) == 0 {
		parts = []expr.Expr{v}
	}
	for _, p := range f.Exclude {
		parts = append(parts, patternTest(v, p, true))
	}
	return andAll(parts...)
}

// triggerRule builds the workflow rule for one trigger; ok is false when GitLab has no pipeline source for it.
func triggerRule(t *ir.Trigger) (cond expr.Expr, ok bool) {
	source := func(s string) expr.Expr { return binary(expr.OpEq, ref1(expr.CtxEvent), strLit(s)) }
	switch t.Kind {
	case ir.TriggerPush:
		return filterCond(ref1(expr.CtxBranch), t.Branches), true
	case ir.TriggerTag:
		return filterCond(ref1(expr.CtxTag), t.Tags), true
	case ir.TriggerPR:
		c := source(sourceMR)
		if hasFilter(t.Branches) {
			c = andAll(c, filterCond(expr.Ref{Path: []string{"pr", "base_ref"}}, t.Branches))
		}
		return c, true
	case ir.TriggerSchedule:
		return source("schedule"), true
	case ir.TriggerManual:
		return source("web"), true
	case ir.TriggerAPI:
		return source("api"), true
	}
	return nil, false
}

func hasFilter(f ir.Filter) bool { return len(f.Include) > 0 || len(f.Exclude) > 0 }

func defaultTriggers() []ir.Trigger {
	return []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
}

func sameTriggers(a, b []ir.Trigger) bool {
	wrap := func(ts []ir.Trigger) *ir.Project {
		return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "x", Triggers: ts}}}
	}
	ok, _ := ir.Equal(wrap(a), wrap(b))
	return ok
}

// workflow writes workflow:rules from the triggers. The original block is restored when it still says what the triggers say.
func (e *emitter) workflow(pl *ir.Pipeline) (*yaml.Node, error) {
	pp := "pipeline/" + pl.ID
	for i := range pl.Triggers {
		e.triggerNotes(triggerPath(pl.ID, i), &pl.Triggers[i])
	}
	raw := extOf(pl.Ext)["workflow"]
	if raw != nil {
		if n, err := plainNode(raw); err == nil {
			s := newState(&report.Report{})
			if ts, ok, _ := s.workflowTriggers(n); ok && sameTriggers(ts, pl.Triggers) {
				return n, nil
			}
		}
	}
	if len(pl.Triggers) == 0 || (raw == nil && sameTriggers(pl.Triggers, defaultTriggers())) {
		return nil, nil
	}
	rules := seqOf()
	for i := range pl.Triggers {
		t := &pl.Triggers[i]
		cond, ok := triggerRule(t)
		if !ok {
			continue
		}
		text, err := exprgitlab.Print(cond)
		if err != nil {
			e.lose(triggerPath(pl.ID, i), triggerFilterFeature(t), "filter has no GitLab form: "+err.Error())
			continue
		}
		r := newMap()
		r.set("if", str(text))
		if paths := e.pathFilter(pp, triggerPath(pl.ID, i), t); len(paths) > 0 {
			r.set("changes", strSeq(paths))
		}
		rules.Content = append(rules.Content, r.n)
	}
	if len(rules.Content) == 0 {
		rules.Content = append(rules.Content, ruleNever())
	}
	w := newMap()
	w.set("rules", rules)
	return w.n, nil
}

func triggerFilterFeature(t *ir.Trigger) caps.Feature {
	if t.Kind == ir.TriggerTag {
		return caps.TriggerFilterTags
	}
	return caps.TriggerFilterBranch
}

// pathFilter returns the rules:changes list; excludes have no GitLab form.
func (e *emitter) pathFilter(_, tp string, t *ir.Trigger) []string {
	if len(t.Paths.Exclude) > 0 {
		e.lose(tp, caps.TriggerFilterPaths, "rules:changes has no exclude patterns")
	}
	return t.Paths.Include
}

func (e *emitter) triggerNotes(tp string, t *ir.Trigger) {
	switch t.Kind {
	case ir.TriggerSchedule:
		e.external(tp, caps.TriggerSchedule, "pipeline schedule in project settings", fmt.Sprintf(scheduleInstruction, t.Cron))
	case ir.TriggerAPI:
		e.external(tp, caps.TriggerAPI, "pipeline trigger token or API call", apiInstruction)
	case ir.TriggerPipelineDone:
		e.lose(tp, caps.TriggerPipelineDone, "GitLab starts a downstream pipeline from a trigger job in the upstream project, which this file cannot declare")
	case ir.TriggerResource:
		e.lose(tp, caps.TriggerResource, "GitLab CI has no resource triggers")
	}
	if t.Kind == ir.TriggerPush && hasFilter(t.Tags) {
		e.lose(tp, caps.TriggerFilterTags, "a push trigger with tag filters has no GitLab form; use a tag trigger")
	}
}
