package drone

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	keyWhen    = "when"
	keyTrigger = "trigger"
	keyPaths   = "paths"

	eventPush   = "push"
	eventPR     = "pull_request"
	eventTag    = "tag"
	eventCron   = "cron"
	eventCustom = "custom"
)

// triggerKinds maps the Drone events that are a pure trigger to IR trigger kinds.
var triggerKinds = map[string]ir.TriggerKind{
	eventPush: ir.TriggerPush, eventPR: ir.TriggerPR, eventTag: ir.TriggerTag,
	eventCron: ir.TriggerSchedule, eventCustom: ir.TriggerManual,
}

// pathsSpec is the paths filter of a trigger.
type pathsSpec struct {
	filter ir.Filter
	line   int
}

// trigger is a parsed trigger block. cond is its Job.If form; pure is its pipeline trigger form, or nil when
// the block is more than a trigger filter.
type trigger struct {
	cond  expr.Expr
	extra map[string]any
	lines map[string]int
	paths *pathsSpec
	// report notes an opaque condition; it runs only when the condition lands on the job.
	report func()
	// extraKeys lists the keys of extra in order.
	extraKeys []string
	pure      []ir.Trigger
}

// condJSON writes a condition value as JSON, a form that drone.Parse reads and an Opaque holds. Scalars keep
// their source text, and $$ becomes $, because Drone substitutes the raw YAML before it parses.
func condJSON(v any) ([]byte, error) { return json.Marshal(condValue(v)) }

func condValue(v any) any {
	switch t := v.(type) {
	case *omap:
		out := make(map[string]any, len(t.keys))
		for _, k := range t.keys {
			out[k] = condValue(t.vals[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = condValue(e)
		}
		return out
	case scal:
		if t.null() {
			return nil
		}
		return unescape(t.text())
	}
	return nil
}

// opaque keeps the whole condition as Drone text: it never becomes "no condition", so a Drone round trip is exact.
func opaque(raw []byte) expr.Expr { return expr.Opaque{Dialect: dexpr.Dialect, Text: string(raw)} }

// noteOpaque reports a condition that is kept as Drone text.
func (d *doc) noteOpaque(k *sink, key, why string, line int) {
	k.rep.Add(report.Entry{
		IRPath: k.path, Feature: caps.ExprOpaque, Level: caps.Lost, Strategy: "kept as opaque Drone text",
		Detail: key + " " + why, SourceLoc: fmt.Sprintf("%s:%d", d.file, d.base+line),
	})
}

// parsedCond is a when or trigger value after parsing. cond is nil when the conditions match every run. parsed is
// false when drone.Parse rejected the value; opaque is true when cond keeps the whole value as text, so rest is
// inside it. rest holds the keys that have no expression form.
type parsedCond struct {
	cond expr.Expr
	// why says why the condition is opaque.
	why  string
	rest []string
	c    dexpr.Conditions
	// parsed is false when drone.Parse rejected the value.
	parsed bool
	opaque bool
}

func (d *doc) read(v any) (parsedCond, error) {
	raw, err := condJSON(v)
	if err != nil {
		return parsedCond{}, err
	}
	if hasRepeat(v) {
		return parsedCond{cond: opaque(raw), why: "repeats a key; the last value is kept as opaque Drone text", opaque: true}, nil
	}
	c, rest, perr := dexpr.Parse(raw)
	if perr != nil {
		return parsedCond{cond: opaque(raw), why: "has a form that Drone reads but the parser does not model (" + perr.Error() + ")", opaque: true}, nil
	}
	e, eerr := dexpr.ToExpr(c)
	if eerr != nil {
		return parsedCond{cond: opaque(raw), why: "has no canonical expression (" + eerr.Error() + ")", c: c, rest: rest, parsed: true, opaque: true}, nil
	}
	return parsedCond{cond: e, c: c, rest: rest, parsed: true}, nil
}

// hasRepeat reports whether a mapping inside v repeats a key.
func hasRepeat(v any) bool {
	switch t := v.(type) {
	case *omap:
		if t.repeated {
			return true
		}
		for _, e := range t.vals {
			if hasRepeat(e) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasRepeat(e) {
				return true
			}
		}
	}
	return false
}

// condition reads a step when into its condition. Keys with no expression form go to Ext under key.
func (d *doc) condition(k *sink, key string, v any, line int) (expr.Expr, error) {
	p, err := d.read(v)
	if err != nil {
		return nil, err
	}
	if p.opaque {
		d.noteOpaque(k, key, p.why, line)
		return p.cond, nil
	}
	if len(p.rest) == 0 {
		return p.cond, nil
	}
	m, _ := asMap(v)
	left := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for _, name := range p.rest {
		left.set(name, m.vals[name], m.lines[name])
	}
	k.vals[key] = plain(left)
	for _, name := range left.keys {
		k.note(caps.ExtPassthrough, key+"."+name+" has no IR field; kept in Ext", left.lines[name])
	}
	return p.cond, nil
}

// readTrigger reads the pipeline trigger block.
func (d *doc) readTrigger(v any, line int) (*trigger, error) {
	p, err := d.read(v)
	if err != nil {
		return nil, err
	}
	t := &trigger{cond: p.cond, lines: map[string]int{}}
	if p.opaque {
		t.report = func() { d.noteOpaque(d.ext, keyTrigger, p.why, line) }
	}
	m, isMap := asMap(v)
	if !p.parsed || !isMap {
		return t, nil
	}
	if !p.opaque && len(p.rest) > 0 {
		t.extra = map[string]any{}
		for _, name := range p.rest {
			t.extra[name] = plain(m.vals[name])
			t.extraKeys = append(t.extraKeys, name)
			t.lines[name] = m.lines[name]
		}
	}
	if pv, has := m.vals[keyPaths]; has {
		if f, isFilter := pathFilter(pv); isFilter {
			t.paths = &pathsSpec{filter: f, line: m.lines[keyPaths]}
		}
	}
	if t.pure = pureTriggers(p.c, p.rest, t.paths); t.pure == nil {
		t.paths = nil
	}
	return t, nil
}

// pathFilter reads a paths value: a name, a list of names, or an include/exclude mapping.
func pathFilter(v any) (ir.Filter, bool) {
	if l, ok := textList(v); ok {
		return ir.Filter{Include: unescapeAll(l)}, true
	}
	m, ok := asMap(v)
	if !ok {
		return ir.Filter{}, false
	}
	var f ir.Filter
	for _, k := range m.keys {
		l, ok := textList(m.vals[k])
		if !ok {
			return ir.Filter{}, false
		}
		switch k {
		case "include":
			f.Include = unescapeAll(l)
		case "exclude":
			f.Exclude = unescapeAll(l)
		default:
			return ir.Filter{}, false
		}
	}
	return f, true
}

func unescapeAll(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = unescape(s)
	}
	return out
}

// pureTriggers returns the pipeline triggers for conditions that are a pure trigger filter, else nil: a literal
// event list, a branch filter and a paths filter, with no ref, repo, status or other key. Branch and paths apply
// to push and pull request events only.
func pureTriggers(c dexpr.Conditions, rest []string, paths *pathsSpec) []ir.Trigger {
	empty := func(f dexpr.Filter) bool { return len(f.Include) == 0 && len(f.Exclude) == 0 }
	if !empty(c.Ref) || !empty(c.Repo) || c.Status != nil || len(c.Event.Exclude) > 0 || len(c.Event.Include) == 0 {
		return nil
	}
	for _, k := range rest {
		if k != keyPaths || paths == nil {
			return nil
		}
	}
	filtered := !empty(c.Branch) || paths != nil
	var out []ir.Trigger
	seen := map[string]bool{}
	for _, ev := range c.Event.Include {
		kind, ok := triggerKinds[ev]
		if !ok || (filtered && ev != eventPush && ev != eventPR) {
			return nil
		}
		if seen[ev] {
			continue
		}
		seen[ev] = true
		tr := ir.Trigger{Kind: kind}
		if filtered {
			tr.Branches = ir.Filter{Include: slices.Clone(c.Branch.Include), Exclude: slices.Clone(c.Branch.Exclude)}
			if paths != nil {
				tr.Paths = paths.filter
			}
		}
		out = append(out, tr)
	}
	return out
}
