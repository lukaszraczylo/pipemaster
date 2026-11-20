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
	return out
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
		spec.runsOn = l
		w.rep.Add(report.Entry{
			IRPath: w.jobPath(), Feature: caps.JobIf, Level: caps.Emulated, Strategy: "read as when.status",
			Detail: "runs_on is deprecated in Woodpecker; its list is read as when.status", SourceLoc: fmt.Sprintf("%s:%d", w.file, r.line("runs_on")),
		})
	}
	return spec, nil
}

// exactGlob reports whether a doublestar pattern means the same as the glob an IR filter holds: a literal, or
// one with single stars only. A path pattern may also end in /**, which both read as "below this directory"
// for a file path. Any other doublestar form differs (a/** also matches a, ? and {} have other meanings).
func exactGlob(p string, isPath bool) bool {
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
		if it.item.Evaluate != "" || !it.item.Ref.Empty() || !it.item.Repo.Empty() || !it.item.Instance.Empty() ||
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

func triggerEqual(a, b ir.Trigger) bool {
	return a.Kind == b.Kind && slices.Equal(a.Branches.Include, b.Branches.Include) && slices.Equal(a.Branches.Exclude, b.Branches.Exclude) &&
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
