package gitlab

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
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

const negPrefix = "!"

// filterCond folds the ordered include list the way GHA does: a plain entry adds its matches, a "!" entry removes
// them, and the last entry that names a ref wins. Excludes then remove their matches. With no plain entry the
// ref itself must be set.
func filterCond(v expr.Expr, f ir.Filter) expr.Expr {
	var cond expr.Expr
	for _, p := range f.Include {
		glob, neg := strings.CutPrefix(p, negPrefix)
		switch {
		case !neg && cond == nil:
			cond = patternTest(v, glob, false)
		case !neg:
			cond = binary(expr.OpOr, cond, patternTest(v, glob, false))
		case cond != nil:
			cond = binary(expr.OpAnd, cond, patternTest(v, glob, true))
		}
	}
	if cond == nil {
		cond = v
		for _, p := range f.Include {
			if g, ok := strings.CutPrefix(p, negPrefix); ok {
				cond = binary(expr.OpAnd, cond, patternTest(v, g, true))
			}
		}
	}
	for _, p := range f.Exclude {
		cond = binary(expr.OpAnd, cond, patternTest(v, p, true))
	}
	return cond
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

// workflow writes workflow:name and workflow:rules; a nil result means the file needs no workflow block.
func (e *emitter) workflow(pl *ir.Pipeline) (*yaml.Node, error) {
	n, err := e.workflowRules(pl)
	if err != nil || !pipelineName(pl.Name) {
		return n, err
	}
	name := str(pl.Name)
	if n == nil {
		w := newMap()
		w.set("name", name)
		return w.n, nil
	}
	if cur := get(n, "name"); cur != nil {
		*cur = *name
		return n, nil
	}
	n.Content = append([]*yaml.Node{str("name"), name}, n.Content...)
	return n, nil
}

// workflowRules writes workflow:rules from the triggers. The original block is restored when it still says what the triggers say.
func (e *emitter) workflowRules(pl *ir.Pipeline) (*yaml.Node, error) {
	for i := range pl.Triggers {
		e.triggerNotes(triggerPath(pl.ID, i), &pl.Triggers[i])
		e.foreignExt(triggerPath(pl.ID, i), pl.Triggers[i].Ext)
	}
	raw := extOf(pl.Ext)["workflow"]
	if raw != nil {
		n, err := plainNode(raw)
		if err != nil {
			return nil, err
		}
		s := newState(&report.Report{})
		ts, ok, _ := s.workflowTriggers(n)
		// Parse gives the default triggers to a block it cannot model, so the block is still the source.
		if (ok && sameTriggers(ts, pl.Triggers)) || (!ok && sameTriggers(pl.Triggers, defaultTriggers())) {
			return n, nil
		}
	}
	if e.noTriggers(pl) {
		e.loseNoTriggers(pl)
		w := newMap()
		w.set("rules", seqOf(ruleNever()))
		return w.n, nil
	}
	if len(pl.Triggers) == 0 || (raw == nil && sameTriggers(pl.Triggers, defaultTriggers())) {
		return nil, nil
	}
	rules := seqOf()
	for i := range pl.Triggers {
		if r, _ := e.triggerRuleNode(pl.ID, i, &pl.Triggers[i]); r != nil {
			rules.Content = append(rules.Content, r)
		}
	}
	if len(rules.Content) == 0 {
		rules.Content = append(rules.Content, ruleNever())
	}
	w := newMap()
	w.set("rules", rules)
	return w.n, nil
}

// triggerRuleNode is the workflow rule for trigger i of a pipeline, with a key that names its text so callers
// can drop duplicates. It is nil when GitLab has no form for the trigger.
func (e *emitter) triggerRuleNode(pid string, i int, t *ir.Trigger) (*yaml.Node, string) {
	cond, ok := triggerRule(t)
	if !ok {
		return nil, ""
	}
	tp := triggerPath(pid, i)
	text, err := exprgitlab.Print(cond)
	if err != nil {
		e.lose(tp, triggerFilterFeature(t), "filter has no GitLab form: "+err.Error())
		return nil, ""
	}
	r := newMap()
	r.set("if", str(text))
	key := text
	if paths := e.pathFilter("pipeline/"+pid, tp, t); len(paths) > 0 {
		r.set("changes", strSeq(paths))
		key += " changes " + strings.Join(paths, ",")
	}
	return r.n, key
}

func triggerFilterFeature(t *ir.Trigger) caps.Feature {
	if t.Kind == ir.TriggerTag {
		return caps.TriggerFilterTags
	}
	return caps.TriggerFilterBranch
}

// pathFilter returns the rules:changes list; excludes, including "!" entries, have no GitLab form.
func (e *emitter) pathFilter(_, tp string, t *ir.Trigger) []string {
	var include []string
	excluded := len(t.Paths.Exclude) > 0
	for _, p := range t.Paths.Include {
		if strings.HasPrefix(p, negPrefix) {
			excluded = true
			continue
		}
		include = append(include, p)
	}
	if excluded {
		e.lose(tp, caps.TriggerFilterPaths, "rules:changes has no exclude patterns")
	}
	return include
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

var triggerFeatures = map[ir.TriggerKind]caps.Feature{
	ir.TriggerPush: caps.TriggerPush, ir.TriggerPR: caps.TriggerPullRequest, ir.TriggerTag: caps.TriggerTag,
	ir.TriggerSchedule: caps.TriggerSchedule, ir.TriggerManual: caps.TriggerManual, ir.TriggerAPI: caps.TriggerAPI,
	ir.TriggerPipelineDone: caps.TriggerPipelineDone, ir.TriggerResource: caps.TriggerResource,
}

// triggerMarks carries the pipeline's triggers in the file when workflow:rules would not read back as them.
func (e *emitter) triggerMarks(pl *ir.Pipeline, w *yaml.Node) ([]string, error) {
	if len(pl.Triggers) == 0 {
		return nil, nil
	}
	got, ok := defaultTriggers(), true
	if w != nil {
		got, ok, _ = newState(&report.Report{}).workflowTriggers(w)
	}
	if ok && sameTriggers(got, pl.Triggers) {
		return nil, nil
	}
	var out []string
	for i, t := range pl.Triggers {
		line, err := markerArg(triggerWire(t))
		if err != nil {
			return nil, err
		}
		out = append(out, emitkit.Marker(triggerFeatures[t.Kind], triggerPath(pl.ID, i)), line)
	}
	return out, nil
}

// wireTrigger is a trigger as a marker carries it: no Ext, which round-trip metadata keeps.
type wireTrigger struct {
	Resource *ir.ResourceRef `json:"resource,omitempty"`
	Kind     ir.TriggerKind  `json:"kind"`
	Cron     string          `json:"cron,omitempty"`
	Branches ir.Filter       `json:"branches,omitzero"`
	Tags     ir.Filter       `json:"tags,omitzero"`
	Paths    ir.Filter       `json:"paths,omitzero"`
}

func triggerWire(t ir.Trigger) wireTrigger {
	return wireTrigger{Resource: t.Resource, Kind: t.Kind, Cron: t.Cron, Branches: t.Branches, Tags: t.Tags, Paths: t.Paths}
}

func (w wireTrigger) trigger() ir.Trigger {
	return ir.Trigger{Resource: w.Resource, Kind: w.Kind, Cron: w.Cron, Branches: w.Branches, Tags: w.Tags, Paths: w.Paths}
}

// noTriggers is true for a pipeline from another provider that starts on no event GitLab models. Without a
// workflow guard such a file would run on every push and tag.
func (e *emitter) noTriggers(pl *ir.Pipeline) bool { return len(pl.Triggers) == 0 && e.from != nil }

// loseNoTriggers reports the events a no-trigger pipeline had; GitLab has no pipeline source for them.
func (e *emitter) loseNoTriggers(pl *ir.Pipeline) {
	var events []string
	foreign, _ := pl.Ext[e.from.ID].(map[string]any)
	if m, ok := foreign["events"].(map[string]any); ok {
		events = slices.Sorted(maps.Keys(m))
	}
	detail := "the pipeline has no trigger GitLab can express; workflow:rules never starts it"
	if len(events) > 0 {
		detail += " (source events: " + strings.Join(events, ", ") + ")"
	}
	e.lose("pipeline/"+pl.ID, caps.ExtPassthrough, detail)
}
