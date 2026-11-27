package vars

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// The prelude is a prefix of the first script line, so no line index of the script moves. The marker starts
// it and the end token closes it; Strip cuts everything between them.
const (
	startMark = "export PIPEMASTER_VARS="
	endMark   = ": pipemaster-end;"
)

const (
	eventsKey  = "events"
	canonical  = provider.GHA
	lostFormat = ": \"${%s:?pipemaster: lost: variable %s has no %s source}\""
)

// Result holds the report entries of one Prepare call.
type Result struct{ entries, retained []report.Entry }

// Report adds the entries to rep.
func (r *Result) Report(rep *report.Report) {
	if r == nil || rep == nil {
		return
	}
	for _, e := range r.entries {
		rep.Add(e)
	}
	rep.Retained = append(rep.Retained, r.retained...)
}

// Prepare returns p with a variable prelude on each script step that reads a predefined variable that target
// does not set. It copies what it changes and leaves p as it was.
func Prepare(p *ir.Project, target string) (*ir.Project, *Result) {
	res := &Result{}
	t, err := Load()
	if p == nil || err != nil {
		return p, res
	}
	paths := map[*ir.Step]string{}
	jobPaths := map[*ir.Job]string{}
	for _, n := range ir.Nodes(p) {
		switch {
		case n.Step != nil:
			paths[n.Step] = n.Path
		case n.Job != nil:
			jobPaths[n.Job] = n.Path
		}
	}
	pr := &preparer{stepLost: map[*ir.Step][]string{}, t: t, target: target, origin: p.Origin.Provider, res: res, paths: paths, jobPaths: jobPaths}
	out := *p
	out.Pipelines = make([]*ir.Pipeline, len(p.Pipelines))
	for i, pl := range p.Pipelines {
		out.Pipelines[i] = pr.pipeline(pl)
	}
	return &out, res
}

type preparer struct {
	t        *Table
	res      *Result
	stepLost map[*ir.Step][]string
	paths    map[*ir.Step]string
	jobPaths map[*ir.Job]string
	kinds    map[string]bool
	target   string
	origin   string
	pushOnly bool
}

func (pr *preparer) pipeline(pl *ir.Pipeline) *ir.Pipeline {
	if pl == nil {
		return nil
	}
	cp := *pl
	cp.Jobs = pr.jobs(pl, pl.Jobs)
	cp.Finally = pr.jobs(pl, pl.Finally)
	return &cp
}

func (pr *preparer) jobs(pl *ir.Pipeline, jobs []*ir.Job) []*ir.Job {
	if jobs == nil {
		return nil
	}
	out := make([]*ir.Job, len(jobs))
	for i, j := range jobs {
		if j == nil {
			continue
		}
		pr.kinds = admitted(pl, j)
		pr.pushOnly = len(pr.kinds) == 1 && pr.kinds[kindPush]
		cp := *j
		jobLost := pr.jobFields(j, &cp)
		cp.Steps = make([]*ir.Step, len(j.Steps))
		for k, st := range j.Steps {
			cp.Steps[k] = pr.step(pr.stepFields(st), append(slices.Clone(pl.Env), j.Env...), jobLost)
		}
		out[i] = &cp
	}
	return out
}

func scriptStep(st *ir.Step) bool {
	return st != nil && len(st.Script) > 0 && st.Action == nil && !emitkit.IsFailLoud(st.Script)
}

func (pr *preparer) step(st *ir.Step, outer []ir.Var, fieldLost []string) *ir.Step {
	if st != nil && st.Image != nil {
		fieldLost = append(slices.Clone(fieldLost), pr.stepLost[st]...)
	}
	if !scriptStep(st) {
		return st
	}
	env, order := effective(outer, st.Env)
	text := strings.Join(st.Script, "\n")
	var names []string
	bare := map[string]bool{}
	for _, r := range ScanRefs(text, true) {
		names = appendNew(names, r.Name)
		bare[r.Name] = r.Bare
	}
	var reexport []string
	expand := provider.Family(pr.target) != provider.GitLab
	for _, name := range order {
		v := env[name]
		if v.Kind != ir.ValueLit || strings.ContainsRune(v.Lit, '\n') {
			continue
		}
		refs := slices.DeleteFunc(Refs(v.Lit, false), func(n string) bool { return !pr.t.predefined(n) })
		if len(refs) == 0 {
			continue
		}
		for _, n := range refs {
			names = appendNew(names, n)
			bare[n] = true
		}
		if expand {
			reexport = append(reexport, name)
		}
	}
	tagName := ""
	if slices.ContainsFunc(fieldLost, func(l string) bool { return strings.HasPrefix(l, armMark+kindTag+"|") }) {
		tagName = pr.originName("tag")
		names = appendNew(names, tagName)
	}
	var guards, defs, defined, lost, after []string
	for _, n := range names {
		if _, user := env[n]; user || pr.t.skip[n] || pr.t.Native(n, pr.origin, pr.target) {
			continue
		}
		rhs, ok := pr.resolve(n)
		switch {
		case ok:
			defs = append(defs, pr.define(n, rhs))
			defined = append(defined, n)
			if g, has := pr.prGuard(n); has {
				guards = append(guards, g)
				lost = append(lost, n+" on pull request events")
			}
		case pr.t.foreign(n, pr.origin, pr.target):
			// A script that sets a default for the name copes with it being unset, so it gets no guard.
			if bare[n] {
				guards = append(guards, fmt.Sprintf(lostFormat, n, n, pr.target))
				lost = append(lost, n)
			}
		}
	}
	for _, name := range reexport {
		defs = append(defs, fmt.Sprintf("export %s=\"%s\"", name, quoteValue(env[name].Lit, pr.origin)))
	}
	for _, l := range fieldLost {
		if arm, isArm := strings.CutPrefix(l, armMark); isArm {
			if g, ok := pr.armGuard(arm, tagName); ok {
				after = append(after, g)
			} else if kind, name, _ := strings.Cut(arm, "|"); kind == kindTag {
				pr.note(st, caps.Lost, "", "variable "+name+" in a field has another value on tag events on "+pr.target+", and "+pr.origin+" has no tag variable to test at run time, so the step has no guard for tag events and runs with the wrong value")
			}
			continue
		}
		guards = append(guards, fmt.Sprintf("printf '%%s\\n' 'pipemaster: lost: %s' >&2; exit 1", l))
	}
	defs = append(defs, after...)
	if len(guards)+len(defs) == 0 {
		return st
	}
	cp := *st
	cp.Script = slices.Clone(st.Script)
	if !place(cp.Script, startMark+fmt.Sprint(len(guards)+len(defs))+"; "+strings.Join(append(guards, defs...), "; ")+"; "+endMark) {
		pr.note(st, caps.Lost, "", "the script starts with a shebang line and has no other line, so the variable prelude has no place")
		return st
	}
	pr.report(st, defined, reexport, lost)
	return &cp
}

// place puts prefix before the first script line that is not a shebang.
func place(script []string, prefix string) bool {
	for i, entry := range script {
		lines := strings.Split(entry, "\n")
		for k, l := range lines {
			if strings.HasPrefix(l, "#!") {
				continue
			}
			lines[k] = prefix + " " + l
			script[i] = strings.Join(lines, "\n")
			return true
		}
	}
	return false
}

func (pr *preparer) report(st *ir.Step, defined, reexport, lost []string) {
	if len(defined)+len(reexport) > 0 {
		var parts []string
		if len(defined) > 0 {
			parts = append(parts, "the script reads "+strings.Join(defined, ", ")+", which "+pr.target+" does not set; a prelude defines them from the "+pr.target+" values")
		}
		if len(reexport) > 0 {
			parts = append(parts, pr.target+" does not expand variable values, so the prelude sets "+strings.Join(reexport, ", ")+" again in the shell")
		}
		pr.note(st, caps.Emulated, "variable prelude", strings.Join(parts, "; "))
	}
	if len(lost) > 0 {
		pr.note(st, caps.Lost, "", "the script reads "+strings.Join(lost, ", ")+", which "+pr.target+" has no source for; the step stops with a message when it starts")
	}
}

func (pr *preparer) note(st *ir.Step, level caps.Level, strategy, detail string) {
	pr.res.entries = append(pr.res.entries, report.Entry{
		IRPath: pr.paths[st], Feature: caps.ScriptInterpolation, Level: level, Strategy: strategy, Detail: detail,
	})
}

// resolve gives the shell text that holds the variable called name on the target.
func (pr *preparer) resolve(name string) (string, bool) {
	o, ok := pr.t.ownerFor(name, pr.origin)
	if !ok {
		return "", false
	}
	e := pr.t.raw.Vars[o.id]
	if e.Values != "" {
		return pr.eventValue(o)
	}
	return pr.t.spell(o.id, pr.target, 0)
}

// eventValue maps an event value of the provider that owns the name to the target's value.
func (pr *preparer) eventValue(o owner) (string, bool) {
	cur, ok := pr.t.spell(o.id, pr.target, 0)
	if !ok {
		return "", false
	}
	col := provider.Family(pr.target)
	if templated[col] {
		col = canonical
	}
	var cases []string
	seen := map[string]bool{}
	keys := make([]string, 0, len(pr.t.raw.Events))
	for k := range pr.t.raw.Events {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		row := pr.t.raw.Events[k]
		if len(row[o.provider]) == 0 {
			continue
		}
		from := row[o.provider][0]
		for _, to := range row[col] {
			if seen[to] {
				continue
			}
			seen[to] = true
			cases = append(cases, "("+to+") printf %s "+from+";;")
		}
	}
	cases = append(cases, "(*) printf %s "+cur+";;")
	return "`case " + cur + " in " + strings.Join(cases, " ") + " esac`", true
}

// predefined reports whether name is a predefined variable of any provider.
func (t *Table) predefined(name string) bool {
	if t.skip[name] {
		return false
	}
	if _, ok := t.owner[name]; ok {
		return true
	}
	for _, k := range t.known {
		if k[name] {
			return true
		}
	}
	return false
}

// effective merges the variable lists, later ones winning, and returns the names in order of first use.
func effective(lists ...[]ir.Var) (map[string]ir.Value, []string) {
	env := map[string]ir.Value{}
	var order []string
	for _, l := range lists {
		for _, v := range l {
			if _, seen := env[v.Name]; !seen {
				order = append(order, v.Name)
			}
			env[v.Name] = v.Value
		}
	}
	return env, order
}

func appendNew(l []string, s string) []string {
	if slices.Contains(l, s) {
		return l
	}
	return append(l, s)
}

// quoteValue writes a variable value as the inside of a double-quoted shell string that still expands $NAME.
// Only a GitLab value keeps "$$" as an escape for one "$"; elsewhere "$$" is two literal dollars.
func quoteValue(v, origin string) string {
	pair := `\$\$`
	if provider.Family(origin) == provider.GitLab {
		pair = `\$`
	}
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$(", `\$(`, "$$", pair).Replace(v)
}

// Strip removes the prelude that Prepare wrote from the scripts of p.
func Strip(p *ir.Project) {
	if p == nil {
		return
	}
	for _, n := range ir.Nodes(p) {
		if n.Step == nil {
			continue
		}
		for i, entry := range n.Step.Script {
			if strings.Contains(entry, startMark) {
				n.Step.Script[i] = stripEntry(entry)
			}
		}
	}
}

func stripEntry(entry string) string {
	lines := strings.Split(entry, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, startMark) {
			continue
		}
		if k := strings.Index(l, endMark); k >= 0 {
			lines[i] = strings.TrimPrefix(l[k+len(endMark):], " ")
		}
	}
	return strings.Join(lines, "\n")
}

// originName is the first name that the origin gives to the entry id.
func (pr *preparer) originName(id string) string {
	if ns := pr.t.raw.Vars[id].Names[provider.Family(pr.origin)]; len(ns) > 0 {
		return ns[0]
	}
	return ""
}

// armGuard gives the run-time guard for the tag arm of a field spelling: the step stops on a tag event with a
// message. arm is "<kind>|<name>".
func (pr *preparer) armGuard(arm, tagName string) (string, bool) {
	kind, name, _ := strings.Cut(arm, "|")
	if kind != kindTag || tagName == "" {
		return "", false
	}
	msg := fmt.Sprintf("printf '%%s\\n' 'pipemaster: lost: variable %s in a field has another value on tag events on %s' >&2; exit 1", name, pr.target)
	return fmt.Sprintf("if [ -n \"${%s}\" ]; then %s; fi", tagName, msg), true
}

// prGuard gives the run-time guard for a name whose value on a pull request has no equal on the target: the
// step stops on that event with a message.
func (pr *preparer) prGuard(name string) (string, bool) {
	o, ok := pr.t.ownerFor(name, pr.origin)
	if !ok {
		return "", false
	}
	e := pr.t.raw.Vars[o.id]
	in := func(p string) bool { return slices.Contains(e.LostOnPR, provider.Family(p)) }
	if len(e.LostOnPR) == 0 || in(o.provider) == in(pr.target) {
		return "", false
	}
	ev, ok := pr.t.spell("event", pr.target, 0)
	if !ok {
		return "", false
	}
	col := provider.Family(pr.target)
	if templated[col] {
		col = canonical
	}
	var vals []string
	for _, v := range pr.t.raw.Events["pull_request"][col] {
		vals = append(vals, v)
	}
	if len(vals) == 0 {
		return "", false
	}
	return fmt.Sprintf("case %s in %s) printf '%%s\\n' 'pipemaster: lost: variable %s has no %s source on pull request events' >&2; exit 1;; esac",
		ev, strings.Join(vals, "|"), name, pr.target), true
}

const (
	kindPush     = "push"
	kindPR       = "pr"
	kindTag      = "tag"
	kindSchedule = "schedule"
	kindManual   = "manual"
	kindOther    = "other"
	armMark      = "\x00arm|"
)

// defaultKinds are the events of a pipeline that has no trigger. Drone runs a pipeline with no event filter on
// every event: drone-yaml yaml/cond.go Condition.Match (lines 42-53) accepts any value when Include is empty, and
// drone v2.25.0 trigger/skip.go skipEvent (lines 32-34) asks only that.
var defaultKinds = []string{kindPush, kindPR, kindTag, kindSchedule, kindManual}

// admitted lists the event kinds on which job j of pl can run. The triggers give the base set, and a job
// condition that needs a branch removes the other kinds: a tag run has no branch, and Drone matches a branch
// filter against the target of the run (drone v2.25.0 trigger/skip.go:24-26).
func admitted(pl *ir.Pipeline, j *ir.Job) map[string]bool {
	kinds := map[string]bool{}
	if pl == nil || len(pl.Triggers) == 0 {
		for _, k := range defaultKinds {
			kinds[k] = true
		}
	} else {
		for _, tr := range pl.Triggers {
			switch tr.Kind {
			case ir.TriggerPush:
				kinds[kindPush] = true
				if len(tr.Tags.Include) > 0 || len(tr.Tags.Exclude) > 0 {
					kinds[kindTag] = true
				}
			case ir.TriggerPR:
				kinds[kindPR] = true
			case ir.TriggerTag:
				kinds[kindTag] = true
			case ir.TriggerSchedule:
				kinds[kindSchedule] = true
			case ir.TriggerManual:
				kinds[kindManual] = true
			default:
				kinds[kindOther] = true
			}
		}
	}
	if j != nil && j.If != nil && requiresBranch(j.If) {
		for k := range kinds {
			if k != kindPush && k != kindPR {
				delete(kinds, k)
			}
		}
	}
	return kinds
}

// requiresBranch reports whether e is false on every run that has no branch.
func requiresBranch(e expr.Expr) bool {
	switch v := e.(type) {
	case expr.Binary:
		switch v.Op {
		case expr.OpAnd:
			return requiresBranch(v.L) || requiresBranch(v.R)
		case expr.OpOr:
			return requiresBranch(v.L) && requiresBranch(v.R)
		case expr.OpEq, expr.OpMatch:
			return isBranchRef(v.L) || isBranchRef(v.R)
		}
	case expr.Call:
		return len(v.Args) > 0 && isBranchRef(v.Args[0])
	case expr.Opaque:
		return strings.HasPrefix(v.Text, "when.branch")
	}
	return false
}

func isBranchRef(e expr.Expr) bool {
	r, ok := e.(expr.Ref)
	return ok && len(r.Path) == 1 && r.Path[0] == expr.CtxBranch
}

// fieldRefs rewrites the variable references of a field that no script runs in, such as an image or a cache
// key. A name that has the same meaning on the target is spelled in the target's own syntax. A name whose value
// depends on the event gets the target's event-keyed spelling where the table has one. Any other predefined name
// of another provider has no source in a field, so it is returned.
func (pr *preparer) fieldRefs(text string) (string, []string, []string) {
	var lost, arms []string
	for _, n := range Refs(text, false) {
		if !pr.t.foreign(n, pr.origin, pr.target) {
			continue
		}
		spelling, kinds, ok := pr.fieldSpelling(n)
		if !ok {
			lost = append(lost, n)
			continue
		}
		for _, k := range kinds {
			arms = append(arms, k+"|"+n)
		}
		re := regexp.MustCompile(`\$\{` + regexp.QuoteMeta(n) + `\}|\$` + regexp.QuoteMeta(n) + `\b`)
		text = re.ReplaceAllLiteralString(text, spelling)
	}
	return text, lost, arms
}

// fieldSpelling is the text that stands for the name n in a field of the target. kinds lists the admitted events
// on which that text has another value than the name has at the source; the step stops on those events. The
// field is lost when an event outside the guarded kinds differs.
func (pr *preparer) fieldSpelling(n string) (spelling string, kinds []string, ok bool) {
	o, found := pr.t.ownerFor(n, pr.origin)
	if !found {
		return "", nil, false
	}
	e := pr.t.raw.Vars[o.id]
	family := provider.Family(pr.target)
	if ns := e.Names[family]; len(ns) > 0 && (family == provider.Drone || family == provider.Woodpecker) {
		return "${{ " + family + "." + ns[0] + " }}", nil, true
	}
	if f := e.Field[family]; f != "" {
		listed, limited := e.FieldEvents[family]
		if !limited {
			listed, limited = e.FieldEvents["default"]
		}
		for _, k := range []string{kindTag, kindOther, kindPush, kindPR, kindSchedule, kindManual} {
			if pr.kinds[k] && limited && !slices.Contains(listed, k) {
				if k != kindTag {
					return "", nil, false
				}
				kinds = append(kinds, k)
			}
		}
		return f, kinds, true
	}
	canon := e.Canon
	if e.PushCanon != "" && pr.pushOnly {
		canon = e.PushCanon
	}
	if canon == "" || len(e.LostOnPR) > 0 || (e.ScriptText && !pr.pushOnly) {
		return "", nil, false
	}
	return "${{ " + canon + " }}", nil, true
}

// field applies fieldRefs to one text and reports what is lost at path. A rewritten text is kept in the metadata, so
// the return trip reads the source text again.
func (pr *preparer) field(text, path string, f caps.Feature) (string, []string) {
	out, lost, arms := pr.fieldRefs(text)
	var msgs []string
	for _, a := range arms {
		k, n, _ := strings.Cut(a, "|")
		msgs = append(msgs, armMark+a)
		pr.res.entries = append(pr.res.entries, report.Entry{
			IRPath: path, Feature: f, Level: caps.Lost,
			Detail: "the field reads " + n + ", which has another value on " + k + " events on " + pr.target + "; the steps that use it stop on those events with a message",
		})
	}
	for _, n := range lost {
		msg := fmt.Sprintf("%s variable %s has no %s source", f, n, pr.target)
		msgs = append(msgs, msg)
		pr.res.entries = append(pr.res.entries, report.Entry{
			IRPath: path, Feature: f, Level: caps.Lost,
			Detail: "the field reads " + n + ", which " + pr.target + " has no source for in a field; the steps that use it stop with a message",
		})
	}
	if out != text && len(lost) == 0 {
		pr.res.retained = append(pr.res.retained, report.Entry{IRPath: path, Feature: f, Level: caps.Lost, Detail: "variable spelled for " + pr.target})
	}
	return out, msgs
}

// jobFields rewrites the job-level fields of cp, which is a copy, and returns the messages for the steps.
func (pr *preparer) jobFields(orig, cp *ir.Job) []string {
	path := pr.jobPaths[orig]
	var msgs []string
	if cp.Container != nil {
		c := *cp.Container
		var m []string
		c.Ref, m = pr.field(c.Ref, path, caps.JobContainer)
		cp.Container, msgs = &c, append(msgs, m...)
	}
	if len(cp.Services) > 0 {
		cp.Services = slices.Clone(cp.Services)
		for i := range cp.Services {
			var m []string
			cp.Services[i].Image.Ref, m = pr.field(cp.Services[i].Image.Ref, path, caps.JobServices)
			msgs = append(msgs, m...)
		}
	}
	if len(cp.Cache) > 0 {
		cp.Cache = slices.Clone(cp.Cache)
		for i := range cp.Cache {
			var m []string
			cp.Cache[i].Key, m = pr.field(cp.Cache[i].Key, path, caps.JobCache)
			msgs = append(msgs, m...)
			cp.Cache[i].FallbackKeys = slices.Clone(cp.Cache[i].FallbackKeys)
			for k := range cp.Cache[i].FallbackKeys {
				cp.Cache[i].FallbackKeys[k], m = pr.field(cp.Cache[i].FallbackKeys[k], path, caps.JobCache)
				msgs = append(msgs, m...)
			}
		}
	}
	if len(cp.Artifacts) > 0 {
		cp.Artifacts = slices.Clone(cp.Artifacts)
		for i := range cp.Artifacts {
			paths := slices.Clone(cp.Artifacts[i].Paths)
			for k := range paths {
				var m []string
				paths[k], m = pr.field(paths[k], path, caps.JobArtifacts)
				msgs = append(msgs, m...)
			}
			cp.Artifacts[i].Paths = paths
		}
	}
	return msgs
}

// stepFields rewrites the image of a step and keeps its messages for step.
func (pr *preparer) stepFields(st *ir.Step) *ir.Step {
	if st == nil || st.Image == nil {
		return st
	}
	ref, msgs := pr.field(st.Image.Ref, pr.paths[st], caps.StepImage)
	if ref == st.Image.Ref && len(msgs) == 0 {
		return st
	}
	cp := *st
	img := *st.Image
	img.Ref = ref
	cp.Image = &img
	pr.paths[&cp] = pr.paths[st]
	pr.stepLost[&cp] = msgs
	return &cp
}

// define writes the assignment of name. A command substitution gets its own export, so that the shell does not
// hide the exit status of the command, and the $( ) form unless the target reads $( as its own syntax.
func (pr *preparer) define(name, rhs string) string {
	if !strings.Contains(rhs, "`") {
		return fmt.Sprintf("export %s=\"%s\"", name, rhs)
	}
	if provider.Family(pr.target) != provider.Tekton {
		var b strings.Builder
		open := true
		for _, c := range rhs {
			switch {
			case c != '`':
				b.WriteRune(c)
			case open:
				b.WriteString("$(")
				open = false
			default:
				b.WriteByte(')')
				open = true
			}
		}
		rhs = b.String()
	}
	return fmt.Sprintf("%s=\"%s\"; export %s", name, rhs, name)
}
