package woodpecker

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const (
	keyPath      = "path"
	keyStatus    = "status"
	keyEvaluate  = "evaluate"
	statusOK     = "success"
	statusFail   = "failure"
	refTagPrefix = "refs/tags/"
	negation     = "!"
)

// whenEntry is one when list item: its keys are ANDed, and items are ORed.
type whenEntry struct {
	wpexpr.Item
	path   wpexpr.Filter
	status []string
}

func (w whenEntry) empty() bool {
	return w.Evaluate == "" && w.Event.Empty() && w.Branch.Empty() && w.Ref.Empty() && w.Repo.Empty() &&
		w.Instance.Empty() && w.path.Empty() && len(w.status) == 0
}

func filterNode(f wpexpr.Filter) *yaml.Node {
	escAll := func(vs []string) []string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = esc(v)
		}
		return out
	}
	if len(f.Exclude) == 0 {
		if vs := escAll(f.Include); len(vs) == 1 {
			return str(vs[0])
		}
		return strSeq(escAll(f.Include))
	}
	// The object form takes lists only (path.include and path.exclude in the pinned schema).
	m := newMap()
	if len(f.Include) > 0 {
		m.set("include", strSeq(escAll(f.Include)))
	}
	m.set("exclude", strSeq(escAll(f.Exclude)))
	return m.n
}

func (w whenEntry) node() *yaml.Node {
	m := newMap()
	for _, k := range []struct {
		key string
		f   wpexpr.Filter
	}{
		{wpexpr.KeyEvent, w.Event}, {wpexpr.KeyBranch, w.Branch}, {wpexpr.KeyRef, w.Ref},
		{wpexpr.KeyRepo, w.Repo}, {wpexpr.KeyInstance, w.Instance}, {keyPath, w.path},
	} {
		if !k.f.Empty() {
			m.set(k.key, filterNode(k.f))
		}
	}
	if len(w.status) > 0 {
		m.set(keyStatus, strSeq(w.status))
	}
	if w.Evaluate != "" {
		m.set(keyEvaluate, str(esc(w.Evaluate)))
	}
	return m.n
}

// whenNode renders entries as a when list; nil when nothing restricts the run.
func whenNode(entries []whenEntry) *yaml.Node {
	if len(entries) == 0 || slices.ContainsFunc(entries, whenEntry.empty) {
		return nil
	}
	n := seqOf()
	for _, w := range entries {
		n.Content = append(n.Content, w.node())
	}
	return n
}

// neverEntries is the fail-safe for a condition Woodpecker cannot state: the construct never runs.
// It keeps the event filters of base, so a trigger list still reads back from the file.
func neverEntries(base []whenEntry) []whenEntry {
	if len(base) == 0 {
		return []whenEntry{{Item: wpexpr.Item{Evaluate: "false"}}}
	}
	out := slices.Clone(base)
	for i := range out {
		out[i].Evaluate = "false"
	}
	return out
}

// splitStatus pulls the always, failure and success calls out of an AND chain. ok is false when a status
// function sits anywhere else or cancelled() is used, which Woodpecker has no step state for.
func splitStatus(x expr.Expr) (rest expr.Expr, status []string, ok bool) {
	var conj []expr.Expr
	flatten(x, &conj)
	var kept []expr.Expr
	seen := ""
	for _, c := range conj {
		call, isCall := c.(expr.Call)
		if !isCall || len(call.Args) != 0 {
			kept = append(kept, c)
			continue
		}
		switch call.Name {
		case "always", statusFail, statusOK:
			if seen != "" && seen != call.Name {
				return nil, nil, false
			}
			seen = call.Name
		case "cancelled":
			return nil, nil, false
		default:
			kept = append(kept, c)
		}
	}
	if slices.ContainsFunc(kept, hasStatusCall) {
		return nil, nil, false
	}
	for _, k := range kept {
		if rest == nil {
			rest = k
			continue
		}
		rest = expr.Binary{Op: expr.OpAnd, L: rest, R: k}
	}
	switch seen {
	case "always":
		status = []string{statusOK, statusFail}
	case statusFail:
		status = []string{statusFail}
	}
	return rest, status, true
}

// hasSuccessCall reports whether an AND chain holds an explicit success() call.
func hasSuccessCall(x expr.Expr) bool {
	var conj []expr.Expr
	flatten(x, &conj)
	return slices.ContainsFunc(conj, func(c expr.Expr) bool {
		call, ok := c.(expr.Call)
		return ok && call.Name == statusOK && len(call.Args) == 0
	})
}

func flatten(x expr.Expr, out *[]expr.Expr) {
	if b, ok := x.(expr.Binary); ok && b.Op == expr.OpAnd {
		flatten(b.L, out)
		flatten(b.R, out)
		return
	}
	*out = append(*out, x)
}

func hasStatusCall(x expr.Expr) bool {
	switch v := x.(type) {
	case expr.Call:
		switch v.Name {
		case "always", statusFail, statusOK, "cancelled":
			return true
		}
		return slices.ContainsFunc(v.Args, hasStatusCall)
	case expr.Unary:
		return hasStatusCall(v.X)
	case expr.Binary:
		return hasStatusCall(v.L) || hasStatusCall(v.R)
	}
	return false
}

// condition translates x from the source dialect into when entries and a status list. ok is false when
// Woodpecker cannot state it; the caller then disables the construct.
func (e *emitter) condition(path string, f caps.Feature, x expr.Expr, what string) (items []wpexpr.Item, status []string, ok bool) {
	rest, status, ok := splitStatus(x)
	if !ok {
		e.disable(path, f, x, what)
		return nil, nil, false
	}
	if status == nil && hasSuccessCall(x) {
		e.lose(path, f, "success() is the Woodpecker default, so the file reads back with no "+what+" condition")
	}
	if rest == nil {
		return nil, status, true
	}
	t := rest
	if o, isOpaque := rest.(expr.Opaque); isOpaque && o.Dialect != wpexpr.Dialect {
		e.disable(path, f, x, what)
		return nil, nil, false
	}
	if e.from != nil {
		if t, ok = expr.TranslateFacts(rest, *e.from, wpexpr.Lang, e.facts); !ok {
			e.disable(path, f, x, what)
			return nil, nil, false
		}
	}
	items, ok = wpexpr.SplitWhen(t)
	if !ok {
		e.disable(path, f, x, what)
		return nil, nil, false
	}
	return items, status, true
}

func (e *emitter) disable(path string, f caps.Feature, x expr.Expr, what string) {
	e.lose(path, f, "condition not expressible in Woodpecker CI; "+what+" disabled")
	if expr.HasOpaque(x) && f != caps.ExprOpaque {
		e.lose(path, caps.ExprOpaque, "an opaque expression of another dialect has no Woodpecker form")
	}
	e.comment(string(f) + " " + path + ": " + expr.String(x))
}

// translate is the translation of a condition that the file carries, and whether the trigger facts shaped it: a
// result that differs from the plain translation, or one that only exists with the facts.
func (e *emitter) translate(x expr.Expr) (t expr.Expr, usedFacts, ok bool) {
	t, ok = expr.TranslateFacts(x, *e.from, wpexpr.Lang, e.facts)
	if !ok {
		return t, false, false
	}
	plain, plainOK := expr.Translate(x, *e.from, wpexpr.Lang)
	return t, !plainOK || expr.String(plain) != expr.String(t), true
}

// sourceArg is the marker argument that carries a condition in its source dialect; ok is false when the
// translation kept the text, so a return conversion needs no help.
func (e *emitter) sourceArg(x expr.Expr) (a srcArg, usedFacts, ok bool) {
	if e.from == nil || hasStatusCall(x) {
		return srcArg{}, false, false
	}
	t, usedFacts, ok := e.translate(x)
	if !ok || expr.String(t) == expr.String(x) {
		return srcArg{}, false, false
	}
	return srcArg{Src: expr.String(x), From: e.from.ID, Branch: e.facts.Branch}, usedFacts, true
}

// jobSource carries a job condition in its source dialect only when the trigger facts shaped its translation:
// without them the translation reads back as written.
func (e *emitter) jobSource(x expr.Expr) (srcArg, bool) {
	a, usedFacts, ok := e.sourceArg(x)
	return a, ok && usedFacts
}

// notePullRequestTest reports a condition whose translation differs from the plain one: Woodpecker holds the
// target branch on a pull request, so the written condition carries a pull request test that the source does not
// have. Without the round-trip metadata it comes back with that test.
func (e *emitter) notePullRequestTest(path string, f caps.Feature, used bool) {
	if used {
		e.external(path, f, "pull request test", "Woodpecker holds the target branch in CI_COMMIT_BRANCH on a pull request, so the condition gains a pull request test; the source condition is kept in a marker")
	}
}

// markSource records a condition in its source dialect when the translation changed its text, so a return
// conversion restores it.
func (e *emitter) markSource(path string, f caps.Feature, x expr.Expr) {
	a, usedFacts, ok := e.sourceArg(x)
	if !ok {
		return
	}
	e.notePullRequestTest(path, f, usedFacts)
	if err := e.markArg(f, path, a); err != nil {
		e.comment("expression source not carried: " + err.Error())
	}
}

// stepWhen renders a step condition as a when list; nil means no restriction.
func (e *emitter) stepWhen(path string, x expr.Expr) *yaml.Node {
	if x == nil {
		return nil
	}
	items, status, ok := e.condition(path, caps.StepIf, x, "the step")
	if !ok {
		return whenNode(neverEntries(nil))
	}
	e.markSource(path, caps.StepIf, x)
	entries := make([]whenEntry, 0, len(items))
	for _, it := range items {
		entries = append(entries, whenEntry{Item: it, status: status})
	}
	if len(entries) == 0 && len(status) > 0 {
		entries = []whenEntry{{status: status}}
	}
	return whenNode(entries)
}

// irGlobs maps IR glob lists to a when filter. A leading ! means "not" in GHA patterns and is written as an exclude.
// inexact lists each pattern whose Woodpecker (doublestar) meaning can differ from the IR meaning.
func irGlobs(f ir.Filter, isPath bool) (out wpexpr.Filter, inexact []string) {
	for _, p := range f.Include {
		if rest, neg := strings.CutPrefix(p, negation); neg {
			out.Exclude = append(out.Exclude, rest)
			inexact = append(inexact, p)
			continue
		}
		out.Include = append(out.Include, p)
		if !exactGlob(p, isPath) {
			inexact = append(inexact, p)
		}
	}
	for _, p := range f.Exclude {
		out.Exclude = append(out.Exclude, p)
		if !exactGlob(p, isPath) {
			inexact = append(inexact, p)
		}
	}
	return out, inexact
}

// regexQuantifiers are the GHA pattern characters that doublestar reads as plain text or as another operator.
const regexQuantifiers = "+?"

func needsRegex(f ir.Filter) bool {
	return slices.ContainsFunc(append(slices.Clone(f.Include), f.Exclude...), func(p string) bool {
		return strings.ContainsAny(strings.TrimPrefix(p, negation), regexQuantifiers)
	})
}

// hasPlus is true when a pattern uses + as a quantifier, which doublestar reads as a plain character, so the
// pattern would never match a real name. A ? still matches there, only with another meaning.
func hasPlus(f ir.Filter) bool {
	return slices.ContainsFunc(append(slices.Clone(f.Include), f.Exclude...), func(p string) bool { return strings.Contains(p, "+") })
}

// ghaRegex turns a GHA filter pattern into an anchored RE2 pattern: * stops at a slash, ** does not, ? and +
// quantify the character before them, and a character class stays one.
func ghaRegex(p string) (string, bool) {
	var sb strings.Builder
	sb.WriteString("^")
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '*':
			if i+1 < len(rs) && rs[i+1] == '*' {
				sb.WriteString(".*")
				i++
			} else {
				sb.WriteString("[^/]*")
			}
		case '?', '+':
			if i == 0 {
				return "", false
			}
			sb.WriteRune(c)
		case '[':
			end := slices.Index(rs[i:], ']')
			if end < 0 {
				return "", false
			}
			sb.WriteString(string(rs[i : i+end+1]))
			i += end
		case '\\':
			if i+1 >= len(rs) {
				return "", false
			}
			i++
			sb.WriteString(regexp.QuoteMeta(string(rs[i])))
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	if _, err := regexp.Compile(sb.String()); err != nil {
		return "", false
	}
	return sb.String(), true
}

// regexTest writes a filter as a matches test on ref: a run passes when it matches an include and no exclude.
func regexTest(ref string, f ir.Filter) (string, bool) {
	r := expr.Ref{Path: []string{ref}}
	var inc, exc []expr.Expr
	add := func(p string, negate bool) bool {
		rx, ok := ghaRegex(p)
		if !ok {
			return false
		}
		if negate {
			exc = append(exc, expr.Binary{Op: expr.OpNoMatch, L: r, R: expr.Lit{Kind: expr.LitStr, Str: rx}})
		} else {
			inc = append(inc, expr.Binary{Op: expr.OpMatch, L: r, R: expr.Lit{Kind: expr.LitStr, Str: rx}})
		}
		return true
	}
	for _, p := range f.Include {
		rest, neg := strings.CutPrefix(p, negation)
		if !add(rest, neg) {
			return "", false
		}
	}
	for _, p := range f.Exclude {
		if !add(p, true) {
			return "", false
		}
	}
	var out expr.Expr
	if len(inc) > 0 {
		out = inc[0]
		for _, x := range inc[1:] {
			out = expr.Binary{Op: expr.OpOr, L: out, R: x}
		}
	}
	for _, x := range exc {
		out = and(out, x)
	}
	text, err := wpexpr.Print(out)
	return text, err == nil
}

// gateArg says that the evaluate text of the one when entry is the job condition, not part of a trigger. It also
// carries the condition in its source dialect when the translation changed its text.
type gateArg struct {
	Paths *changesArg `json:"paths,omitempty"`
	srcArg
	Gate bool `json:"gate"`
}

// evalArg carries the evaluate text an emitter wrote for a trigger filter, so the parser can tell it from a condition.
type evalArg struct {
	Evaluate string `json:"evaluate"`
}

// regexFilter writes the filter of one trigger as an evaluate test when doublestar cannot read its patterns. It
// returns the evaluate text; "false" with a lost entry is the fail-safe when no test exists.
func (e *emitter) regexFilter(tp string, feat caps.Feature, ref string, f ir.Filter) string {
	if e.inexact == "" {
		e.inexact = feat
	}
	text, ok := regexTest(ref, f)
	if !ok {
		e.lose(tp, feat, "a pattern has no regular expression form, so the workflow is disabled for this trigger")
		return disabledText
	}
	e.emulate(tp, feat, "evaluate matches", "pattern with + or ? has no doublestar form; it is written as a regular expression test on "+ref)
	if err := e.markArg(feat, tp, evalArg{Evaluate: text}); err != nil {
		e.comment("trigger filter text not carried: " + err.Error())
	}
	return text
}

func andText(a, b string) string {
	switch {
	case a == disabledText || b == disabledText:
		return disabledText
	case a == "":
		return b
	case b == "":
		return a
	}
	return "(" + a + ") && (" + b + ")"
}

func (e *emitter) globFilter(tp string, f caps.Feature, in ir.Filter, isPath bool) wpexpr.Filter {
	out, inexact := irGlobs(in, isPath)
	if len(inexact) > 0 {
		if e.inexact == "" {
			e.inexact = f
		}
		e.emulate(tp, f, "doublestar glob", "pattern "+strings.Join(inexact, ", ")+" has a different meaning in doublestar than in the source; it is written as is")
	}
	return out
}

// triggerEntries maps the pipeline triggers to when entries, one per trigger, in order.
func (e *emitter) triggerEntries(pl *ir.Pipeline) []whenEntry {
	var out []whenEntry
	for i := range pl.Triggers {
		t := &pl.Triggers[i]
		tp := "pipeline/" + pl.ID + "/trigger/" + strconv.Itoa(i)
		e.foreignExt(tp, t.Ext)
		if w, ok := e.triggerEntry(tp, t); ok {
			out = append(out, w)
		}
	}
	return out
}

func (e *emitter) triggerEntry(tp string, t *ir.Trigger) (whenEntry, bool) {
	var w whenEntry
	switch t.Kind {
	case ir.TriggerPush, ir.TriggerPR:
		w.Event.Include = []string{eventPush}
		if t.Kind == ir.TriggerPR {
			w.Event.Include = []string{eventPR}
		}
		if needsRegex(t.Branches) {
			w.Evaluate = e.regexFilter(tp, caps.TriggerFilterBranch, expr.CtxBranch, t.Branches)
		} else {
			w.Branch = e.globFilter(tp, caps.TriggerFilterBranch, t.Branches, false)
		}
		if hasPlus(t.Paths) {
			e.lose(tp, caps.TriggerFilterPaths, "a path pattern with + has no Woodpecker form, so the workflow is disabled for this trigger")
			w.Evaluate = disabledText
		} else {
			w.path = e.globFilter(tp, caps.TriggerFilterPaths, t.Paths, true)
		}
		if hasFilter(t.Tags) {
			e.lose(tp, caps.TriggerFilterTags, "a "+string(t.Kind)+" trigger has no tag filter; tags belong to a tag trigger")
		}
	case ir.TriggerTag:
		w.Event.Include = []string{eventTag}
		w.Ref, w.Evaluate = e.tagRefs(tp, t.Tags)
		e.dropFilters(tp, t, "a tag event")
	case ir.TriggerSchedule:
		w.Event.Include = []string{eventCron}
		e.external(tp, caps.TriggerSchedule, "when event cron", "Woodpecker keeps the schedule in the repo settings: add a cron job with the schedule \""+t.Cron+"\" under Settings > Cron")
		e.dropFilters(tp, t, "a cron event")
	case ir.TriggerManual:
		w.Event.Include = []string{eventMan}
		e.dropFilters(tp, t, "a manual event")
	default:
		return w, false
	}
	return w, true
}

func hasFilter(f ir.Filter) bool { return len(f.Include) > 0 || len(f.Exclude) > 0 }

// dropFilters reports the branch and path filters of a trigger that Woodpecker does not apply to the event.
func (e *emitter) dropFilters(tp string, t *ir.Trigger, event string) {
	if hasFilter(t.Branches) {
		e.lose(tp, caps.TriggerFilterBranch, "Woodpecker does not apply a branch filter to "+event)
	}
	if hasFilter(t.Paths) {
		e.lose(tp, caps.TriggerFilterPaths, "Woodpecker does not apply a path filter to "+event)
	}
	if t.Kind != ir.TriggerTag && hasFilter(t.Tags) {
		e.lose(tp, caps.TriggerFilterTags, "Woodpecker does not apply a tag filter to "+event)
	}
}

// tagRefs maps tag globs to a ref filter on the full ref; the tag event itself has no tag name filter.
func (e *emitter) tagRefs(tp string, in ir.Filter) (wpexpr.Filter, string) {
	if !hasFilter(in) {
		return wpexpr.Filter{}, ""
	}
	if needsRegex(in) {
		return wpexpr.Filter{}, e.regexFilter(tp, caps.TriggerFilterTags, expr.CtxTag, in)
	}
	f := e.globFilter(tp, caps.TriggerFilterTags, in, false)
	for i, p := range f.Include {
		f.Include[i] = refTagPrefix + p
	}
	for i, p := range f.Exclude {
		f.Exclude[i] = refTagPrefix + p
	}
	e.emulate(tp, caps.TriggerFilterTags, "ref filter refs/tags/<pattern>", "the tag name filter is written as a ref filter on the tag event")
	e.mark(caps.TriggerFilterTags, tp)
	return f, ""
}

// jobWhen combines the trigger entries, the manual gate, the job condition and the status gate of one workflow.
func (e *emitter) jobWhen(pl *ir.Pipeline, j *ir.Job, jp string, finally bool) ([]whenEntry, error) {
	e.inexact = ""
	base := e.triggerEntries(pl)
	if e.inexact != "" {
		if err := e.markTriggers(e.inexact, "pipeline/"+pl.ID, pl.Triggers); err != nil {
			return nil, err
		}
	}
	if len(pl.Triggers) > 0 && len(base) == 0 {
		e.comment("trigger.* " + jp + ": no trigger of the pipeline has a Woodpecker event")
		e.lose(jp, caps.JobIf, "no trigger of the pipeline has a Woodpecker event; the workflow never runs")
		return neverEntries(nil), nil
	}
	var cond []wpexpr.Item
	var status []string
	if j.If != nil && expr.HasChanges(j.If) {
		return e.changesWhen(j, jp, finally, base)
	}
	if j.If != nil {
		var ok bool
		if cond, status, ok = e.condition(jp, caps.JobIf, j.If, "the workflow"); !ok {
			if j.Manual {
				e.lose(jp, caps.JobManual, "a workflow that never runs has no manual start")
			}
			return neverEntries(base), nil
		}
	}
	if j.Manual {
		e.emulate(jp, caps.JobManual, "when event manual", "the workflow runs only when a person starts it from the UI or API")
		if err := e.markTriggers(caps.JobManual, jp, pl.Triggers); err != nil {
			return nil, err
		}
		base = []whenEntry{{Item: wpexpr.Item{Event: wpexpr.Filter{Include: []string{eventMan}}}}}
	}
	if finally && status == nil {
		status = []string{statusOK, statusFail}
	}
	return withStatus(e.mergeCond(jp, j.If, base, cond), status), nil
}

// mergeCond ANDs the condition with every base entry. Two entries cannot be merged key by key, so the
// whole condition goes into the evaluate text of each base entry.
func (e *emitter) mergeCond(jp string, orig expr.Expr, base []whenEntry, cond []wpexpr.Item) []whenEntry {
	if len(cond) == 0 {
		return base
	}
	if len(base) == 0 {
		out := make([]whenEntry, len(cond))
		for i, it := range cond {
			out[i] = whenEntry{Item: it}
		}
		if src, hasSrc := e.jobSource(orig); hasSrc {
			e.notePullRequestTest(jp, caps.JobIf, true)
			if err := e.markArg(caps.JobIf, jp, gateArg{srcArg: src}); err != nil {
				e.comment("job condition marker not carried: " + err.Error())
			}
		}
		return out
	}
	x, err := wpexpr.WhenExpr(cond)
	if err != nil {
		e.disable(jp, caps.JobIf, orig, "the workflow")
		return neverEntries(base)
	}
	if x == nil {
		return base
	}
	text, err := wpexpr.Print(x)
	if err != nil {
		e.disable(jp, caps.JobIf, orig, "the workflow")
		return neverEntries(base)
	}
	out := slices.Clone(base)
	for i := range out {
		out[i].Evaluate = andText(out[i].Evaluate, text)
	}
	src, hasSrc := e.jobSource(orig)
	e.notePullRequestTest(jp, caps.JobIf, hasSrc)
	if len(out) == 1 || hasSrc {
		if err := e.markArg(caps.JobIf, jp, gateArg{srcArg: src, Gate: len(out) == 1}); err != nil {
			e.comment("job condition marker not carried: " + err.Error())
		}
	}
	return out
}

func withStatus(entries []whenEntry, status []string) []whenEntry {
	if len(status) == 0 {
		return entries
	}
	if len(entries) == 0 {
		entries = []whenEntry{{}}
	}
	out := slices.Clone(entries)
	for i := range out {
		out[i].status = status
	}
	return out
}

// completeEvents gives every entry an event filter: woodpecker-cli lint --strict rejects a workflow whose
// when entries lack one. Listing all events keeps the run condition, because a run has exactly one of them.
func completeEvents(entries []whenEntry) []whenEntry {
	if len(entries) == 0 {
		entries = []whenEntry{{}}
	}
	out := slices.Clone(entries)
	for i := range out {
		if out[i].Event.Empty() {
			out[i].Event.Include = slices.Clone(allEvents)
		}
	}
	return out
}

// changesArg marks a workflow whose job condition is a path predicate that the file states as a path filter on its
// push and pull request entries. Synth is true when the emitter made the entries, so the pipeline had no triggers.
type changesArg struct {
	Src   string `json:"src"`
	From  string `json:"from,omitempty"`
	Synth bool   `json:"synth,omitempty"`
}

// fnmatchExact is true for a GitLab glob (Ruby fnmatch with FNM_PATHNAME) that doublestar reads the same: literal
// characters, * and ? inside a segment, **/ as a whole segment and {a,b} lists. A class, an escape or a ** inside a
// segment has no certain equal.
func fnmatchExact(g string) bool {
	if g == "" || strings.ContainsAny(g, `[]\+!`) {
		return false
	}
	for i := 0; i+1 < len(g); i++ {
		if g[i] == '*' && g[i+1] == '*' && (i > 0 && g[i-1] != '/' || !strings.HasPrefix(g[i:], "**/")) {
			return false
		}
	}
	return !strings.HasSuffix(g, "**")
}

// changesWhen states a job condition that is one path predicate as a path filter. Woodpecker applies a path filter
// to push and pull request events only and treats any other event as a match, as GitLab does for changes. A push
// that creates a branch is compared with its own commits, where GitLab counts every path as changed, so the filter
// can skip a run GitLab makes; it never runs one GitLab skips. Any other condition shape is disabled and reported.
func (e *emitter) changesWhen(j *ir.Job, jp string, finally bool, base []whenEntry) ([]whenEntry, error) {
	globs, ok := expr.ChangesGlobs(j.If)
	if !ok || !slices.ContainsFunc(globs, func(string) bool { return true }) || slices.ContainsFunc(globs, func(g string) bool { return !fnmatchExact(g) }) || e.from == nil || e.from.ID != provider.GitLab {
		e.disable(jp, caps.JobIf, j.If, "the workflow")
		if j.Manual {
			e.lose(jp, caps.JobManual, "a workflow that never runs has no manual start")
		}
		return neverEntries(base), nil
	}
	if j.Manual {
		base = []whenEntry{{Item: wpexpr.Item{Event: wpexpr.Filter{Include: []string{eventMan}}}}}
	}
	synth := len(base) == 0
	if synth {
		base = []whenEntry{{}}
	}
	var out []whenEntry
	for _, ent := range base {
		if !ent.path.Empty() || len(ent.Event.Exclude) > 0 {
			e.disable(jp, caps.JobIf, j.If, "the workflow")
			return neverEntries(base), nil
		}
		events := ent.Event.Include
		if ent.Event.Empty() {
			events = allEvents
		}
		var with, without []string
		for _, ev := range events {
			if ev == eventPush || ev == eventPR {
				with = append(with, ev)
			} else {
				without = append(without, ev)
			}
		}
		if len(with) > 0 {
			w := ent
			w.Event.Include = with
			w.path = wpexpr.Filter{Include: slices.Clone(globs)}
			out = append(out, w)
		}
		if len(without) > 0 {
			w := ent
			w.Event.Include = without
			out = append(out, w)
		}
	}
	e.emulate(jp, caps.JobIf, "when path", "the path filter applies to push and pull request events and every other event runs, as GitLab rules:changes does; a push that creates a branch is compared with its own commits")
	if err := e.markArg(caps.JobIf, jp, gateArg{Paths: &changesArg{Src: expr.String(j.If), From: e.from.ID, Synth: synth}}); err != nil {
		e.comment("job condition marker not carried: " + err.Error())
	}
	var status []string
	if finally {
		status = []string{statusOK, statusFail}
	}
	return withStatus(out, status), nil
}
