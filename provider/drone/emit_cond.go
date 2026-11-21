package drone

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	eventPromote = "promote"
	keyTarget    = "target"
	keyCron      = "cron"
	statusOK     = "success"
	statusFail   = "failure"
	// refHeads, refTagsAll and refPull spell the refs of branch pushes, tag pushes and GitHub pull requests.
	refHeads   = "refs/heads/"
	refTagsAll = tagRefPrefix + "**"
	refPull    = "refs/pull/*/head"
	negation   = "!"
)

// condKeyOrder is the order of the keys of a written condition; other keys follow sorted.
var condKeyOrder = []string{dexpr.KeyEvent, dexpr.KeyBranch, dexpr.KeyRef, dexpr.KeyRepo, keyCron, keyTarget, dexpr.KeyStatus}

// condSet is a condition under construction: the keys with an expression form, and keys that only have YAML.
// Its text is already escaped.
type condSet struct {
	raw    map[string]any
	c      dexpr.Conditions
	folded bool
}

const foldCaveat = "Drone compares case-sensitively; GHA ignores case"

func disabledSet() condSet { return condSet{c: dexpr.Disabled()} }

func empty(f dexpr.Filter) bool { return len(f.Include) == 0 && len(f.Exclude) == 0 }

func (s condSet) typed(key string) bool {
	switch key {
	case dexpr.KeyEvent:
		return !empty(s.c.Event)
	case dexpr.KeyBranch:
		return !empty(s.c.Branch)
	case dexpr.KeyRef:
		return !empty(s.c.Ref)
	case dexpr.KeyRepo:
		return !empty(s.c.Repo)
	case dexpr.KeyStatus:
		return len(s.c.Status) > 0
	}
	return false
}

func (s condSet) has(key string) bool { _, ok := s.raw[key]; return ok || s.typed(key) }

func escFilter(f dexpr.Filter) dexpr.Filter {
	return dexpr.Filter{Include: escAll(f.Include), Exclude: escAll(f.Exclude)}
}

func escConditions(c dexpr.Conditions) dexpr.Conditions {
	c.Event, c.Branch, c.Ref, c.Repo = escFilter(c.Event), escFilter(c.Branch), escFilter(c.Ref), escFilter(c.Repo)
	return c
}

// escAny escapes every string of a decoded JSON value.
func escAny(v any) any {
	switch t := v.(type) {
	case string:
		return esc(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = escAny(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = escAny(e)
		}
		return out
	}
	return v
}

// literalFilter reports whether every value of f is an exact name.
func literalFilter(f dexpr.Filter) bool {
	return len(f.Exclude) == 0 && !slices.ContainsFunc(f.Include, func(s string) bool { return !dexpr.IsLiteral(s) })
}

func mergeFilter(a, b dexpr.Filter) (dexpr.Filter, bool) {
	switch {
	case empty(a):
		return b, true
	case empty(b), slices.Equal(a.Include, b.Include) && slices.Equal(a.Exclude, b.Exclude):
		return a, true
	case literalFilter(a) && literalFilter(b):
		var out dexpr.Filter
		for _, v := range a.Include {
			if slices.Contains(b.Include, v) {
				out.Include = append(out.Include, v)
			}
		}
		return out, len(out.Include) > 0
	}
	return dexpr.Filter{}, false
}

// mergeSets ANDs two conditions. It fails when a key holds two different filters that no intersection of exact
// names joins.
func mergeSets(a, b condSet) (condSet, bool) {
	if a.c.IsDisabled() {
		return a, true
	}
	if b.c.IsDisabled() {
		return b, true
	}
	for k := range b.raw {
		if a.has(k) {
			return condSet{}, false
		}
	}
	for k := range a.raw {
		if b.typed(k) {
			return condSet{}, false
		}
	}
	out := condSet{raw: map[string]any{}}
	for _, s := range []condSet{a, b} {
		for k, v := range s.raw {
			out.raw[k] = v
		}
	}
	for _, p := range []struct {
		dst  *dexpr.Filter
		x, y dexpr.Filter
	}{{&out.c.Event, a.c.Event, b.c.Event}, {&out.c.Branch, a.c.Branch, b.c.Branch}, {&out.c.Ref, a.c.Ref, b.c.Ref}, {&out.c.Repo, a.c.Repo, b.c.Repo}} {
		f, ok := mergeFilter(p.x, p.y)
		if !ok {
			return condSet{}, false
		}
		*p.dst = f
	}
	switch {
	case len(a.c.Status) == 0:
		out.c.Status = b.c.Status
	case len(b.c.Status) == 0 || slices.Equal(a.c.Status, b.c.Status):
		out.c.Status = a.c.Status
	default:
		return condSet{}, false
	}
	return out, true
}

// condNode writes the condition as a mapping; extra holds Ext keys of the source that the condition does not
// set. A paths key never enters the output: the Drone OSS server does not evaluate it.
func condNode(s condSet, extra map[string]any) (*yaml.Node, error) {
	all := condFilters(escConditions(s.c))
	for k, v := range s.raw {
		if _, dup := all[k]; !dup {
			all[k] = v
		}
	}
	for k, v := range extra {
		if _, dup := all[k]; !dup && k != keyPaths {
			all[k] = v
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	m := newYMap()
	for _, k := range append(slices.Clone(condKeyOrder), sortedKeys(all)...) {
		v, ok := all[k]
		if !ok {
			continue
		}
		n, err := plainNode(v)
		if err != nil {
			return nil, err
		}
		m.set(k, n)
	}
	return m.n, nil
}

// condFilters is the when mapping of c: every filter as a list, or an include and exclude map.
func condFilters(c dexpr.Conditions) map[string]any {
	all := map[string]any{}
	for _, p := range []struct {
		key string
		f   dexpr.Filter
	}{{dexpr.KeyEvent, c.Event}, {dexpr.KeyBranch, c.Branch}, {dexpr.KeyRef, c.Ref}, {dexpr.KeyRepo, c.Repo}} {
		if !empty(p.f) {
			all[p.key] = filterValue(p.f)
		}
	}
	if len(c.Status) > 0 {
		all[dexpr.KeyStatus] = c.Status
	}
	return all
}

// filterValue writes a filter as a list, or as an include/exclude mapping of lists: the SchemaStore schema reads
// no scalar there, and Drone reads every form.
func filterValue(f dexpr.Filter) any {
	if len(f.Exclude) == 0 {
		return f.Include
	}
	m := map[string]any{"exclude": f.Exclude}
	if len(f.Include) > 0 {
		m["include"] = f.Include
	}
	return m
}

// exprSet turns a canonical condition into Drone conditions. ok is false when Drone has no filter for it.
func (e *emitter) exprSet(x expr.Expr) (condSet, bool) {
	if c, ok := dexpr.FromExpr(x, e.sem); ok {
		return condSet{c: c}, true
	}
	if c, ok := dexpr.FromExpr(x, e.sem, dexpr.AllowFold); ok {
		return condSet{c: c, folded: true}, true
	}
	o, isOpaque := x.(expr.Opaque)
	if !isOpaque || o.Dialect != dexpr.Dialect {
		return condSet{}, false
	}
	if _, _, ok := dexpr.ParseGlob(o); ok {
		return condSet{}, false
	}
	var m map[string]any
	if json.Unmarshal([]byte(o.Text), &m) != nil {
		return condSet{}, false
	}
	return condSet{raw: escAny(m).(map[string]any)}, true
}

// noteFold reports a filter or guard that reads a folded comparison case-sensitively.
func (e *emitter) noteFold(path string, f caps.Feature) {
	e.emulate(path, f, "case-sensitive comparison", foldCaveat)
}

// guardable reports whether x has a shell guard, and whether that guard reads a folded comparison.
func (e *emitter) guardable(x expr.Expr) (ok, folded bool) {
	if dexpr.Guardable(x, e.sem) {
		return true, false
	}
	return dexpr.Guardable(x, e.sem, dexpr.AllowFold), true
}

// disable reports a condition that Drone cannot state; the caller then writes the disabled filter.
func (e *emitter) disable(path string, f caps.Feature, x expr.Expr, what string) {
	e.lose(path, f, "condition not expressible in Drone CI; "+what+" disabled")
	if expr.HasOpaque(x) && f != caps.ExprOpaque {
		e.lose(path, caps.ExprOpaque, "an opaque expression of another dialect has no Drone form")
	}
	e.comment(string(f) + " " + path + ": " + expr.String(x))
}

// stepWhen renders a step condition. guard is set when the condition is a shell guard on a script step; ok is
// false when the step must not be written to run.
func (e *emitter) stepWhen(sp string, st *ir.Step, script bool) (n *yaml.Node, guard bool, err error) {
	ext := extOf(st.Ext)
	extra, _ := ext[keyWhen].(map[string]any)
	if st.If == nil {
		n, err = condNode(condSet{}, extra)
		return n, false, err
	}
	if cs, ok := e.exprSet(st.If); ok {
		if cs.folded {
			e.noteFold(sp, caps.StepIf)
		}
		if err := e.markCond(caps.StepIf, sp, st.If, cs, nil); err != nil {
			return nil, false, err
		}
		n, err = condNode(cs, extra)
		return n, false, err
	}
	if gok, folded := e.guardable(st.If); script && gok {
		e.emulate(sp, caps.StepIf, "shell guard on the first script line", "the condition has no Drone filter; the step shows success instead of skipped")
		if folded {
			e.noteFold(sp, caps.StepIf)
		}
		if err := e.markGuard(caps.StepIf, sp, st.If); err != nil {
			return nil, false, err
		}
		n, err = condNode(condSet{}, extra)
		return n, true, err
	}
	e.disable(sp, caps.StepIf, st.If, "the step")
	n, err = condNode(disabledSet(), nil)
	return n, false, err
}

// condArg carries a condition that the file reads back as another expression: the IR condition, and what the
// parser reads from the file, so the parser can tell an unedited file. Triggers holds the pipeline triggers that
// a job condition merged with.
type condArg struct {
	If       string `json:"if"`
	Back     string `json:"back"`
	Triggers string `json:"triggers,omitempty"`
	// Guard is true when the condition is a shell guard on the script, not a filter.
	Guard bool `json:"guard,omitempty"`
}

// markGuard writes the marker of a shell guard. It holds the condition when the guard reads back as another
// expression, which happens when a folded comparison is printed case-sensitively.
func (e *emitter) markGuard(f caps.Feature, path string, orig expr.Expr) error {
	line, err := e.printGuard(orig)
	if err != nil {
		return err
	}
	back, ok := dexpr.ParseGuard(line)
	if !ok || expr.String(back) == expr.String(orig) {
		e.mark(f, path)
		return nil
	}
	return e.markArg(f, path, condArg{If: expr.String(orig), Back: expr.String(back), Guard: true})
}

// markCond writes the marker that holds a condition with a filter form, when the file does not read back as the
// same expression or when pipeline triggers merged into it.
func (e *emitter) markCond(f caps.Feature, path string, orig expr.Expr, cs condSet, triggers []ir.Trigger) error {
	if _, own := orig.(expr.Opaque); own {
		return nil
	}
	back, err := dexpr.ToExpr(cs.c)
	if err != nil {
		// The parser keeps a condition with no expression form as Drone text.
		raw, jerr := json.Marshal(condFilters(cs.c))
		if jerr != nil {
			return nil
		}
		back = opaque(raw)
	}
	if back == nil || (len(triggers) == 0 && expr.String(back) == expr.String(orig)) {
		return nil
	}
	arg := condArg{If: expr.String(orig), Back: expr.String(back)}
	if len(triggers) > 0 {
		if arg.Triggers, err = carryYAML(triggers); err != nil {
			return err
		}
	}
	return e.markArg(f, path, arg)
}

// eventOf names the Drone event of a trigger kind.
var eventOf = map[ir.TriggerKind]string{
	ir.TriggerPush: eventPush, ir.TriggerPR: eventPR, ir.TriggerTag: eventTag,
	ir.TriggerSchedule: eventCron, ir.TriggerManual: eventCustom,
}

func hasFilter(f ir.Filter) bool { return len(f.Include) > 0 || len(f.Exclude) > 0 }

// irGlobs maps IR glob lists to a filter. A leading ! means "not" in GHA patterns and is written as an exclude.
// inexact lists each pattern whose Drone (doublestar) meaning can differ from the IR meaning.
func irGlobs(f ir.Filter, prefix string) (out dexpr.Filter, inexact []string) {
	for _, p := range f.Include {
		if rest, neg := strings.CutPrefix(p, negation); neg {
			out.Exclude = append(out.Exclude, prefix+rest)
			inexact = append(inexact, p)
			continue
		}
		out.Include = append(out.Include, prefix+p)
		if !exactGlob(p) {
			inexact = append(inexact, p)
		}
	}
	for _, p := range f.Exclude {
		out.Exclude = append(out.Exclude, prefix+p)
		switch {
		case strings.Contains(p, "+"):
			// An excluded plus pattern is as inert as a negated one, so it reads the same way.
			inexact = append(inexact, negation+p)
		case !exactGlob(p):
			inexact = append(inexact, p)
		}
	}
	return out, inexact
}

// exactGlob reports whether a doublestar pattern means the same to the IR consumers as to Drone: a literal
// without a leading "!" or a "+", or one with single stars only.
func exactGlob(p string) bool {
	if strings.HasPrefix(p, negation) || strings.Contains(p, "+") {
		return false
	}
	return !strings.ContainsAny(p, `?[]{}\`) && !strings.Contains(p, "**")
}

// trigs is the pipeline triggers sorted by Drone event, with their paths in the pipeline.
type trigs struct {
	pl   *ir.Pipeline
	push []int
	pr   []int
	tag  []int
	cron []int
	man  []int
}

func (e *emitter) classify(pl *ir.Pipeline) trigs {
	t := trigs{pl: pl}
	for i := range pl.Triggers {
		tr := &pl.Triggers[i]
		tp := triggerPath(pl.ID, i)
		e.foreignExt(tp, tr.Ext)
		switch tr.Kind {
		case ir.TriggerPush:
			t.push = append(t.push, i)
		case ir.TriggerPR:
			t.pr = append(t.pr, i)
		case ir.TriggerTag:
			t.tag = append(t.tag, i)
		case ir.TriggerSchedule:
			t.cron = append(t.cron, i)
		case ir.TriggerManual:
			t.man = append(t.man, i)
		default:
			continue
		}
		e.dropUnapplied(tp, tr)
	}
	return t
}

// dropUnapplied reports the filters that Drone does not apply to the event of the trigger.
func (e *emitter) dropUnapplied(tp string, t *ir.Trigger) {
	if hasFilter(t.Paths) {
		e.lose(tp, caps.TriggerFilterPaths, "the Drone OSS server does not evaluate trigger.paths, so no path filter is written")
	}
	switch t.Kind {
	case ir.TriggerPush, ir.TriggerPR:
		if hasFilter(t.Tags) {
			e.lose(tp, caps.TriggerFilterTags, "a "+string(t.Kind)+" trigger has no tag filter; tags belong to a tag trigger")
		}
	case ir.TriggerTag:
		if hasFilter(t.Branches) {
			e.lose(tp, caps.TriggerFilterBranch, "Drone does not apply a branch filter to a tag event")
		}
	default:
		if hasFilter(t.Branches) {
			e.lose(tp, caps.TriggerFilterBranch, "Drone does not apply a branch filter to a "+string(t.Kind)+" event")
		}
		if hasFilter(t.Tags) {
			e.lose(tp, caps.TriggerFilterTags, "Drone does not apply a tag filter to a "+string(t.Kind)+" event")
		}
	}
}

func (t trigs) events() []string {
	var out []string
	for _, p := range []struct {
		ev  string
		idx []int
	}{{eventPush, t.push}, {eventPR, t.pr}, {eventTag, t.tag}, {eventCron, t.cron}, {eventCustom, t.man}} {
		if len(p.idx) > 0 {
			out = append(out, p.ev)
		}
	}
	return out
}

func (t trigs) filtersOf(idx []int, tags bool) []ir.Filter {
	var out []ir.Filter
	for _, i := range idx {
		if tags {
			out = append(out, t.pl.Triggers[i].Tags)
		} else {
			out = append(out, t.pl.Triggers[i].Branches)
		}
	}
	return out
}

func sameFilters(fs []ir.Filter) bool {
	for _, f := range fs[1:] {
		if !slices.Equal(f.Include, fs[0].Include) || !slices.Equal(f.Exclude, fs[0].Exclude) {
			return false
		}
	}
	return true
}

func anyFilter(fs []ir.Filter) bool { return slices.ContainsFunc(fs, hasFilter) }

// noteGlobs reports the patterns that doublestar reads differently from the IR. A pattern with the GHA "+"
// quantifier matches nothing in Drone, so an include pattern keeps the job from every ref and an exclude pattern
// excludes no ref. Both are lost; the other patterns are emulated. The ledger keeps one detail per feature and
// path at the worst level, so a lost entry also names the other inexact patterns and the written form.
func (e *emitter) noteGlobs(tp string, f caps.Feature, inexact []string) {
	var lost, other []string
	for _, p := range inexact {
		switch {
		case strings.HasPrefix(p, negation) && strings.Contains(p, "+"):
			lost = append(lost, "exclude pattern "+strings.TrimPrefix(p, negation)+" uses GHA syntax that doublestar reads literally; Drone excludes no ref with it, so the job also runs on refs the source excluded")
		case strings.Contains(p, "+") && !strings.HasPrefix(p, negation):
			lost = append(lost, "pattern "+p+" uses GHA syntax that doublestar reads literally; Drone matches no ref with it")
		default:
			other = append(other, p)
		}
	}
	if len(lost) > 0 {
		detail := strings.Join(lost, "; ")
		if f == caps.TriggerFilterTags {
			detail += "; the filter is still written as a ref filter on the tag event (ref filter refs/tags/<pattern>)"
		} else {
			detail += "; the filter is still written as is"
		}
		if len(other) > 0 {
			detail += "; also inexact: " + strings.Join(other, ", ")
		}
		e.lose(tp, f, detail)
		return
	}
	if len(other) > 0 {
		e.emulate(tp, f, "doublestar glob", "pattern "+strings.Join(other, ", ")+" has a different meaning in doublestar than in the source; it is written as is")
	}
}

// markGlobs writes the marker of a trigger filter. It holds the original patterns when the file spells them another way.
func (e *emitter) markGlobs(f caps.Feature, tp string, a globArg, spelledOther bool) {
	if !spelledOther {
		if f == caps.TriggerFilterTags {
			e.mark(f, tp)
		}
		return
	}
	if err := e.markArg(f, tp, a); err != nil {
		e.comment("trigger filter text not carried: " + err.Error())
	}
}

// triggerSet maps the pipeline triggers to one condition. Drone ANDs the keys of a trigger, so triggers join
// only when their filters fit one event list, one branch filter, or one list of refs. ok is false when they do not.
func (e *emitter) triggerSet(t trigs) (cs condSet, ok bool) {
	pl := t.pl
	evs := t.events()
	if len(evs) == 0 {
		return condSet{}, false
	}
	cs.c.Event.Include = evs
	pushPR := append(slices.Clone(t.push), t.pr...)
	branchFs := t.filtersOf(pushPR, false)
	tagFs := t.filtersOf(t.tag, true)
	switch {
	case anyFilter(tagFs) && len(evs) == 1:
		for _, i := range t.tag {
			tp := triggerPath(pl.ID, i)
			f, inexact := irGlobs(pl.Triggers[i].Tags, tagRefPrefix)
			cs.c.Ref.Include = append(cs.c.Ref.Include, f.Include...)
			cs.c.Ref.Exclude = append(cs.c.Ref.Exclude, f.Exclude...)
			e.noteGlobs(tp, caps.TriggerFilterTags, inexact)
			e.emulate(tp, caps.TriggerFilterTags, "ref filter refs/tags/<pattern>", "the tag name filter is written as a ref filter on the tag event")
			e.markGlobs(caps.TriggerFilterTags, tp, globArg{Tags: pl.Triggers[i].Tags}, len(inexact) > 0)
		}
		return cs, true
	case !anyFilter(branchFs) && !anyFilter(tagFs):
	case anyFilter(branchFs) && sameFilters(branchFs) && !slices.ContainsFunc(branchFs, func(f ir.Filter) bool { return !hasFilter(f) }) &&
		len(t.tag)+len(t.cron)+len(t.man) == 0:
		f, inexact := irGlobs(pl.Triggers[pushPR[0]].Branches, "")
		cs.c.Branch = f
		e.noteGlobs(triggerPath(pl.ID, pushPR[0]), caps.TriggerFilterBranch, inexact)
		e.markGlobs(caps.TriggerFilterBranch, triggerPath(pl.ID, pushPR[0]), globArg{Branches: pl.Triggers[pushPR[0]].Branches}, len(inexact) > 0)
	default:
		return e.refSet(t, cs)
	}
	for _, i := range t.cron {
		e.scheduleNote(pl, i)
	}
	for _, i := range t.man {
		tp := triggerPath(pl.ID, i)
		e.emulate(tp, caps.TriggerManual, "trigger event custom", "a person starts the build through the API or drone build create")
		e.mark(caps.TriggerManual, tp)
	}
	if len(evs) == 1 && evs[0] == eventCron {
		names := make([]any, 0, len(t.cron))
		for _, i := range t.cron {
			names = append(names, cronName(pl, i))
		}
		cs.raw = map[string]any{keyCron: names}
	}
	return cs, true
}

func cronName(pl *ir.Pipeline, i int) string {
	return adapter{}.SanitizeID(pl.ID + "-schedule-" + strconv.Itoa(i))
}

// scheduleNote reports that Drone keeps cron jobs outside the file and names the command.
func (e *emitter) scheduleNote(pl *ir.Pipeline, i int) {
	tp := triggerPath(pl.ID, i)
	cmd := "drone cron add <repo> " + cronName(pl, i) + " \"" + cronSeconds(pl.Triggers[i].Cron) + "\""
	e.external(tp, caps.TriggerSchedule, "trigger event cron with the cron job name", "Drone keeps the schedule in the repo settings: run "+cmd)
}

// cronSeconds prefixes a five-field expression with a seconds field, because Drone reads six fields.
func cronSeconds(c string) string {
	if len(strings.Fields(c)) == 5 {
		return "0 " + c
	}
	return c
}

// refSet writes the triggers as a list of refs, which joins filters that one branch filter cannot: a branch
// push is refs/heads/<pattern>, a tag push is refs/tags/<pattern> and a pull request is refs/pull/<n>/head.
func (e *emitter) refSet(t trigs, cs condSet) (condSet, bool) {
	pl := t.pl
	if len(t.cron)+len(t.man) > 0 || slices.ContainsFunc(t.filtersOf(t.pr, false), hasFilter) {
		return condSet{}, false
	}
	cs.c.Branch = dexpr.Filter{}
	add := func(f dexpr.Filter, all string) {
		inc := f.Include
		if len(inc) == 0 {
			inc = []string{all}
		}
		for _, v := range inc {
			if !slices.Contains(cs.c.Ref.Include, v) {
				cs.c.Ref.Include = append(cs.c.Ref.Include, v)
			}
		}
		cs.c.Ref.Exclude = append(cs.c.Ref.Exclude, f.Exclude...)
	}
	for _, i := range t.push {
		f, inexact := irGlobs(pl.Triggers[i].Branches, refHeads)
		e.noteGlobs(triggerPath(pl.ID, i), caps.TriggerFilterBranch, inexact)
		add(f, refHeads+"**")
	}
	for _, i := range t.tag {
		f, inexact := irGlobs(pl.Triggers[i].Tags, tagRefPrefix)
		e.noteGlobs(triggerPath(pl.ID, i), caps.TriggerFilterTags, inexact)
		add(f, refTagsAll)
	}
	if len(t.pr) > 0 {
		add(dexpr.Filter{}, refPull)
	}
	feat := caps.TriggerFilterBranch
	if len(t.push) == 0 {
		feat = caps.TriggerFilterTags
	}
	e.emulate(triggerPath(pl.ID, firstIndex(t)), feat, "ref filter", "the filters of the triggers differ, so each is written as a ref: refs/heads/<pattern>, refs/tags/<pattern>, refs/pull/*/head for a pull request on GitHub")
	if err := e.markTriggers(feat, "pipeline/"+pl.ID, pl.Triggers); err != nil {
		e.comment("trigger filter text not carried: " + err.Error())
	}
	return cs, true
}

func firstIndex(t trigs) int {
	for _, l := range [][]int{t.push, t.pr, t.tag} {
		if len(l) > 0 {
			return l[0]
		}
	}
	return 0
}

// jobCond is the run condition of a document: the trigger block, and a guard when the job condition has no filter.
type jobCond struct {
	guard expr.Expr
	set   condSet
	// has is true when anything restricts the run.
	has bool
}

// jobSet combines the trigger entries, the manual gate, the job condition and the status gate of one document.
func (e *emitter) jobSet(pl *ir.Pipeline, j *ir.Job, jp string, finally bool) (jobCond, error) {
	var out jobCond
	filtered := false
	if len(pl.Triggers) > 0 {
		cs, ok := e.triggerSet(e.classify(pl))
		if !ok {
			e.comment("trigger.* " + jp + ": the triggers of the pipeline have no single Drone condition")
			e.lose(jp, caps.JobIf, "the triggers of the pipeline have no single Drone condition; the document never runs")
			e.loseTriggers(pl)
			return jobCond{set: disabledSet(), has: true}, nil
		}
		out.set, out.has = cs, true
	}
	switch {
	case j.Environment != nil && j.Environment.Approval:
		cs := condSet{c: dexpr.Conditions{Event: dexpr.Filter{Include: []string{eventPromote}}}}
		if j.Environment.Name != "" {
			cs.raw = map[string]any{keyTarget: []any{esc(j.Environment.Name)}}
		}
		e.emulate(jp, caps.JobEnvironmentApproval, "trigger event promote with target", "a person starts the build with drone build promote")
		if err := e.markTriggers(caps.JobEnvironmentApproval, jp, pl.Triggers); err != nil {
			return jobCond{}, err
		}
		out.set, out.has = cs, true
	case j.Manual:
		e.emulate(jp, caps.JobManual, "trigger event custom", "a person starts the build through the API or drone build create; the document no longer runs on its normal events")
		if err := e.markTriggers(caps.JobManual, jp, pl.Triggers); err != nil {
			return jobCond{}, err
		}
		out.set, out.has = condSet{c: dexpr.Conditions{Event: dexpr.Filter{Include: []string{eventCustom}}}}, true
	}
	if j.If != nil {
		cs, ok := e.exprSet(j.If)
		gok, gfolded := e.guardable(j.If)
		switch {
		case ok:
			if cs.folded {
				e.noteFold(jp, caps.JobIf)
			}
			merged, mok := mergeSets(out.set, cs)
			if !mok {
				e.disable(jp, caps.JobIf, j.If, "the document, whose trigger filters cannot join the condition,")
				return jobCond{set: disabledSet(), has: true}, nil
			}
			out.set, out.has, filtered = merged, true, true
		case guardableSteps(j.Steps) && gok:
			e.emulate(jp, caps.JobIf, "shell guard on the first script line of every step", "the condition has no Drone filter; each step shows success instead of skipped")
			if gfolded {
				e.noteFold(jp, caps.JobIf)
			}
			if err := e.markGuard(caps.JobIf, jp, j.If); err != nil {
				return jobCond{}, err
			}
			out.guard = j.If
		default:
			e.disable(jp, caps.JobIf, j.If, "the document")
			e.loseTriggers(pl)
			return jobCond{set: disabledSet(), has: true}, nil
		}
	}
	if finally && len(out.set.c.Status) == 0 {
		out.set.c.Status, out.has = []string{statusOK, statusFail}, true
	}
	if filtered && !j.Manual && (j.Environment == nil || !j.Environment.Approval) {
		if err := e.markCond(caps.JobIf, jp, j.If, out.set, pl.Triggers); err != nil {
			return jobCond{}, err
		}
	}
	return out, nil
}

// loseTriggers reports the pipeline triggers that a disabled document does not hold.
func (e *emitter) loseTriggers(pl *ir.Pipeline) {
	for i := range pl.Triggers {
		e.lose(triggerPath(pl.ID, i), triggerFeature(&pl.Triggers[i]), "the document is disabled, so the trigger is not written")
	}
}

// triggerFeature names the feature that a trigger of the pipeline exercises most specifically.
func triggerFeature(t *ir.Trigger) caps.Feature {
	switch {
	case hasFilter(t.Tags):
		return caps.TriggerFilterTags
	case hasFilter(t.Branches):
		return caps.TriggerFilterBranch
	}
	return map[ir.TriggerKind]caps.Feature{
		ir.TriggerPush: caps.TriggerPush, ir.TriggerPR: caps.TriggerPullRequest, ir.TriggerTag: caps.TriggerTag,
		ir.TriggerSchedule: caps.TriggerSchedule, ir.TriggerManual: caps.TriggerManual, ir.TriggerAPI: caps.TriggerAPI,
		ir.TriggerPipelineDone: caps.TriggerPipelineDone, ir.TriggerResource: caps.TriggerResource,
	}[t.Kind]
}

// guardableSteps reports whether every step that Drone runs has a script, the only step a guard line can open.
func guardableSteps(steps []*ir.Step) bool {
	for _, st := range steps {
		if st == nil || st.Action != nil || st.Kind == ir.StepAction || st.Kind == ir.StepPipelineCall {
			continue
		}
		if st.Kind != ir.StepScript && len(st.Script) == 0 {
			return false
		}
	}
	return true
}
