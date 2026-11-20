package woodpecker

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Event names of a when filter that map to a trigger kind. Push and tag map here and not through
// expressions because woodpecker.Events has no push literal.
const (
	eventPush = "push"
	eventPR   = "pull_request"
	eventTag  = "tag"
	eventCron = "cron"
	eventMan  = "manual"
	whenKey   = "when"
)

// allEvents is every event of the pinned schema (server/model/const.go at v3.20.1). A filter that lists all
// of them restricts nothing.
var allEvents = []string{
	eventPush, eventPR, "pull_request_closed", "pull_request_metadata", eventTag, "release", "deployment", eventCron, eventMan,
}

var triggerKinds = map[string]ir.TriggerKind{
	eventPush: ir.TriggerPush, eventPR: ir.TriggerPR, eventTag: ir.TriggerTag,
	eventCron: ir.TriggerSchedule, eventMan: ir.TriggerManual,
}

// itemInfo is one when entry: the part the expression package models, and what it does not.
type itemInfo struct {
	item wpexpr.Item
	path wpexpr.Filter
	// status is the entry's status list; hasStatus tells an absent list from an empty one.
	status    []string
	extras    []string
	hasStatus bool
}

// whenSpec is a parsed when, kept as written so a lossy part can go to Ext whole.
type whenSpec struct {
	raw    any
	items  []itemInfo
	runsOn []string
	line   int
	// runsLine is the line of runs_on.
	runsLine int
}

func unescapeAll(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = unescape(s)
	}
	return out
}

// filterOf reads a filter value: a scalar, a list, or {include, exclude}. Any other shape is not a filter.
func filterOf(v any) (wpexpr.Filter, bool) {
	if l, ok := textList(v); ok {
		return wpexpr.Filter{Include: unescapeAll(l)}, true
	}
	m, ok := asMap(v)
	if !ok {
		return wpexpr.Filter{}, false
	}
	var f wpexpr.Filter
	for _, k := range m.keys {
		l, ok := textList(m.vals[k])
		switch {
		case !ok:
			return wpexpr.Filter{}, false
		case k == "include":
			f.Include = unescapeAll(l)
		case k == "exclude":
			f.Exclude = unescapeAll(l)
		default:
			return wpexpr.Filter{}, false
		}
	}
	return f, true
}

func parseItem(m *omap) itemInfo {
	var out itemInfo
	for _, k := range m.keys {
		v := m.vals[k]
		var slot *wpexpr.Filter
		switch k {
		case wpexpr.KeyEvent:
			slot = &out.item.Event
		case wpexpr.KeyBranch:
			slot = &out.item.Branch
		case wpexpr.KeyRef:
			slot = &out.item.Ref
		case wpexpr.KeyRepo:
			slot = &out.item.Repo
		case wpexpr.KeyInstance:
			slot = &out.item.Instance
		case "path":
			slot = &out.path
		case "evaluate":
			if t, ok := text(v); ok {
				out.item.Evaluate = unescape(t)
				continue
			}
			out.extras = append(out.extras, k)
			continue
		case "status":
			if l, ok := textList(v); ok {
				out.status, out.hasStatus = l, true
				continue
			}
			out.extras = append(out.extras, k)
			continue
		default:
			out.extras = append(out.extras, k)
			continue
		}
		f, ok := filterOf(v)
		if !ok {
			out.extras = append(out.extras, k)
			continue
		}
		*slot = f
	}
	if out.item.Event.Exclude == nil && listsEveryEvent(out.item.Event.Include) {
		out.item.Event = wpexpr.Filter{}
	}
	return out
}

func listsEveryEvent(include []string) bool {
	return len(include) >= len(allEvents) && !slices.ContainsFunc(allEvents, func(ev string) bool { return !slices.Contains(include, ev) })
}

// parseWhen reads a when value: one entry, or a list of entries.
func parseWhen(v any, line int) (*whenSpec, error) {
	s := &whenSpec{raw: v, line: line}
	switch t := v.(type) {
	case *omap:
		s.items = []itemInfo{parseItem(t)}
	case []any:
		for _, e := range t {
			m, ok := asMap(e)
			if !ok {
				return nil, fmt.Errorf("line %d: a when entry must be a mapping", line)
			}
			s.items = append(s.items, parseItem(m))
		}
	default:
		return nil, fmt.Errorf("line %d: when must be a mapping or a list of mappings", line)
	}
	if _, err := s.condition(); err != nil {
		return nil, fmt.Errorf("line %d: when: %w", line, err)
	}
	return s, nil
}

// when reads the workflow when and the deprecated runs_on, which Woodpecker reads as when.status.
func (w *wf) when(r *reader) (*whenSpec, error) {
	var spec *whenSpec
	if v, ok := r.take(whenKey); ok {
		var err error
		if spec, err = parseWhen(v, r.line(whenKey)); err != nil {
			return nil, err
		}
	}
	if v, ok := r.take("runs_on"); ok {
		l, isList := textList(v)
		if !isList {
			return nil, fmt.Errorf("line %d: runs_on must be a list", r.line("runs_on"))
		}
		if spec == nil {
			spec = &whenSpec{}
		}
		spec.runsOn, spec.runsLine = l, r.line("runs_on")
	}
	return spec, nil
}

// exactGlob reports whether a doublestar pattern means the same to the IR consumers as to Woodpecker: a literal
// without a leading "!" or a "+", or one with single stars only. IR consumers read a leading "!" as negation and
// "+" as a quantifier (GHA filter pattern cheat sheet); doublestar reads both as literal. A path pattern may also
// end in /**, which both read as "below this directory" for a file path. Any other doublestar form differs
// (a/** also matches a, and ? and {} have other meanings).
func exactGlob(p string, isPath bool) bool {
	if strings.HasPrefix(p, "!") || strings.Contains(p, "+") {
		return false
	}
	if isPath {
		if rest, ok := strings.CutSuffix(p, "/**"); ok && rest != "" {
			p = rest
		}
	}
	return !strings.ContainsAny(p, `?[]{}\`) && !strings.Contains(p, "**")
}

func irFilter(f wpexpr.Filter, isPath bool) (ir.Filter, bool) {
	var out ir.Filter
	for _, p := range append(slices.Clone(f.Include), f.Exclude...) {
		if !exactGlob(p, isPath) {
			return out, false
		}
	}
	if len(f.Include) > 0 {
		out.Include = slices.Clone(f.Include)
	}
	if len(f.Exclude) > 0 {
		out.Exclude = slices.Clone(f.Exclude)
	}
	return out, true
}

// triggers maps a when that only filters events, branches and paths to pipeline triggers. It reports false when
// any entry holds something a trigger cannot say.
func (s *whenSpec) triggers() ([]ir.Trigger, bool) {
	if s == nil || len(s.items) == 0 || len(s.runsOn) > 0 {
		return nil, false
	}
	var out []ir.Trigger
	for _, it := range s.items {
		tagRef, tagOK := tagGlobs(it.item.Ref)
		refBlocks := !it.item.Ref.Empty() && (!tagOK || !slices.Equal(it.item.Event.Include, []string{eventTag}))
		if it.item.Evaluate != "" || refBlocks || !it.item.Repo.Empty() || !it.item.Instance.Empty() ||
			it.hasStatus || len(it.extras) > 0 || len(it.item.Event.Exclude) > 0 || len(it.item.Event.Include) == 0 {
			return nil, false
		}
		for _, ev := range it.item.Event.Include {
			kind, ok := triggerKinds[ev]
			if !ok {
				return nil, false
			}
			t := ir.Trigger{Kind: kind}
			switch ev {
			case eventPush, eventPR:
				var okB, okP bool
				if t.Branches, okB = irFilter(it.item.Branch, false); !okB {
					return nil, false
				}
				if t.Paths, okP = irFilter(it.path, true); !okP {
					return nil, false
				}
			case eventTag:
				// The branch filter does not apply to a tag event, so it constrains nothing here.
				if !it.path.Empty() {
					return nil, false
				}
				var okT bool
				if t.Tags, okT = irFilter(tagRef, false); !okT {
					return nil, false
				}
			default:
				if !it.item.Branch.Empty() || !it.path.Empty() {
					return nil, false
				}
			}
			if !slices.ContainsFunc(out, func(o ir.Trigger) bool { return triggerEqual(o, t) }) {
				out = append(out, t)
			}
		}
	}
	return out, true
}

// tagGlobs reads a ref filter whose every value is a tag ref as the tag name globs, the inverse of the emitter's
// tagRefs. An empty filter is a valid empty result.
func tagGlobs(f wpexpr.Filter) (wpexpr.Filter, bool) {
	var out wpexpr.Filter
	for _, p := range f.Include {
		rest, ok := strings.CutPrefix(p, refTagPrefix)
		if !ok || rest == "" {
			return wpexpr.Filter{}, false
		}
		out.Include = append(out.Include, rest)
	}
	for _, p := range f.Exclude {
		rest, ok := strings.CutPrefix(p, refTagPrefix)
		if !ok || rest == "" {
			return wpexpr.Filter{}, false
		}
		out.Exclude = append(out.Exclude, rest)
	}
	return out, true
}

func triggerEqual(a, b ir.Trigger) bool {
	return a.Kind == b.Kind && slices.Equal(a.Tags.Include, b.Tags.Include) && slices.Equal(a.Tags.Exclude, b.Tags.Exclude) && slices.Equal(a.Branches.Include, b.Branches.Include) && slices.Equal(a.Branches.Exclude, b.Branches.Exclude) &&
		slices.Equal(a.Paths.Include, b.Paths.Include) && slices.Equal(a.Paths.Exclude, b.Paths.Exclude)
}

func and(l, r expr.Expr) expr.Expr {
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	return expr.Binary{Op: expr.OpAnd, L: l, R: r}
}

// condition maps the filters of every entry to one expression; a nil result matches every run.
func (s *whenSpec) condition() (expr.Expr, error) {
	items := make([]wpexpr.Item, len(s.items))
	for i, it := range s.items {
		items[i] = it.item
	}
	return wpexpr.WhenExpr(items)
}

// status gives the status call that gates the run, or nil for the default success. lossy is true when entries
// disagree on the status, which one gate cannot say.
func (s *whenSpec) status() (e expr.Expr, lossy bool, err error) {
	var names []string
	missing := false
	for _, it := range s.items {
		if !it.hasStatus {
			missing = true
			continue
		}
		c, err := wpexpr.StatusExpr(it.status)
		if err != nil {
			return nil, false, err
		}
		names = append(names, c.(expr.Call).Name)
	}
	if len(names) == 0 && len(s.runsOn) > 0 {
		c, err := wpexpr.StatusExpr(s.runsOn)
		if err != nil {
			return nil, false, err
		}
		names = []string{c.(expr.Call).Name}
		missing = false
	}
	if len(names) == 0 {
		return nil, false, nil
	}
	if missing || slices.ContainsFunc(names, func(n string) bool { return n != names[0] }) {
		return nil, true, nil
	}
	if names[0] == "success" {
		return nil, false, nil
	}
	return expr.Call{Name: names[0]}, false, nil
}

// lossyKeys lists the when keys the condition cannot carry. Paths count unless a trigger carries them.
func (s *whenSpec) lossyKeys(statusLossy bool) []string {
	var out []string
	add := func(k string) {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	for _, it := range s.items {
		for _, k := range it.extras {
			add(k)
		}
		if !it.path.Empty() {
			add("path")
		}
	}
	if statusLossy {
		add("status")
	}
	return out
}

// gate computes the expression for the run condition and keeps the whole when in sk, with one report entry
// per key the expression cannot carry.
func (s *whenSpec) gate(sk *sink) (expr.Expr, error) {
	e, err := s.condition()
	if err != nil {
		return nil, err
	}
	st, statusLossy, err := s.status()
	if err != nil {
		return nil, err
	}
	s.noteGlobs(sk)
	s.noteRunsOn(sk)
	if lossy := s.lossyKeys(statusLossy); len(lossy) > 0 {
		sk.vals[whenKey] = plain(s.raw)
		for _, k := range lossy {
			sk.note(caps.JobIf, "when."+k+" has no IR form and is not evaluated; the when is kept in Ext", s.line)
		}
	}
	return and(e, st), nil
}

// apply sets the job condition from the workflow when, unless the pipeline triggers already carry it.
func (s *whenSpec) apply(rep *report.Report, j *ir.Job, file string, triggersUsed bool) error {
	if s == nil || triggersUsed {
		return nil
	}
	sk := &sink{rep: rep, vals: map[string]any{}, path: pipelinePth + "/job/" + j.ID, file: file}
	if j.Ext != nil {
		if m, ok := j.Ext[dialect].(map[string]any); ok {
			sk.vals = m
		}
	}
	e, err := s.gate(sk)
	if err != nil {
		return err
	}
	j.If, j.Ext = e, sk.ext()
	return nil
}

// noteGlobs reports each filter value that is a glob: the condition holds it as an opaque node, not as a native test.
func (s *whenSpec) noteGlobs(sk *sink) {
	for _, it := range s.items {
		for _, f := range []wpexpr.Filter{it.item.Event, it.item.Branch, it.item.Ref, it.item.Repo, it.item.Instance} {
			for _, v := range append(slices.Clone(f.Include), f.Exclude...) {
				if wpexpr.IsLiteral(v) {
					continue
				}
				sk.rep.Add(report.Entry{
					IRPath: sk.path, Feature: caps.ExprOpaque, Level: caps.Emulated, Strategy: "opaque glob condition",
					Detail:    "the glob " + v + " has no exact IR form; the condition keeps it as an opaque node",
					SourceLoc: fmt.Sprintf("%s:%d", sk.file, s.line),
				})
			}
		}
	}
}

// noteRunsOn reports what became of the deprecated runs_on: it gates the run only when no entry sets a status.
func (s *whenSpec) noteRunsOn(sk *sink) {
	if len(s.runsOn) == 0 {
		return
	}
	if slices.ContainsFunc(s.items, func(it itemInfo) bool { return it.hasStatus }) {
		sk.vals["runs_on"] = toList(s.runsOn)
		sk.note(caps.JobIf, "runs_on is ignored because when.status is set; kept in Ext", s.runsLine)
		return
	}
	sk.rep.Add(report.Entry{
		IRPath: sk.path, Feature: caps.JobIf, Level: caps.Emulated, Strategy: "read as when.status",
		Detail: "runs_on is deprecated in Woodpecker; its list is read as when.status", SourceLoc: fmt.Sprintf("%s:%d", sk.file, s.runsLine),
	})
}

func toList(l []string) []any {
	out := make([]any, len(l))
	for i, v := range l {
		out[i] = v
	}
	return out
}
