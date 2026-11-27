package woodpecker

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const (
	stepSep         = "/step/"
	placeholderStep = placeholderName
)

var markerPipelineRE = regexp.MustCompile(`^pipeline/[^/]+`)

// marks are the emulation markers of one workflow file, with their argument lines.
type marks struct {
	args map[ir.FeatureUse][]string
	uses []ir.FeatureUse
}

// readMarks collects the markers of a file. The emitter writes the IR pipeline ID into every path, but Parse
// builds one pipeline with a fixed ID, so only that prefix differs. Argument lines escape "$" as "$$".
func readMarks(src []byte) *marks {
	m := &marks{args: map[ir.FeatureUse][]string{}}
	for _, u := range emitkit.ParseMarkers(src) {
		u.IRPath = markerPipelineRE.ReplaceAllLiteralString(u.IRPath, pipelinePth)
		m.uses = append(m.uses, u)
	}
	for u, lines := range emitkit.ParseArgs(src) {
		u.IRPath = markerPipelineRE.ReplaceAllLiteralString(u.IRPath, pipelinePth)
		for _, l := range lines {
			m.args[u] = append(m.args[u], strings.ReplaceAll(l, dollarEsc, dollar))
		}
	}
	return m
}

// collapseCtx is what a collapser works on: the job of the file and the marker with its argument.
type collapseCtx struct {
	rep  *report.Report
	j    *ir.Job
	use  ir.FeatureUse
	args []string
}

// collapser undoes one emulation and reports whether it found the construct the marker names.
type collapser func(c *collapseCtx) bool

// jobCollapsers run in this order: later ones read what earlier ones restore.
var jobCollapsers = []struct {
	run     collapser
	feature caps.Feature
}{
	{collapseRefs, caps.ScriptInterpolation},
	{collapseStepName, caps.DisplayName},
	{collapseContainer, caps.JobContainer},
	{collapseMatrix, caps.JobMatrix},
	{collapseCache, caps.JobCache},
	{collapseArtifacts, caps.JobArtifacts},
	{collapseAllowFailure, caps.JobAllowFailure},
	{collapseNoop, caps.StepScript},
	{collapseLostEnv, caps.ExprOpaque},
	{collapseStepIf, caps.StepIf},
}

// collapseJob undoes the emulations marked in one workflow file. A marker it cannot restore is reported, so the
// construct stays visible as written.
func collapseJob(rep *report.Report, j *ir.Job, m *marks) (pipeEnv []ir.Var) {
	for _, cl := range jobCollapsers {
		for _, u := range m.uses {
			if u.Feature != cl.feature {
				continue
			}
			if !cl.run(&collapseCtx{rep: rep, j: j, use: u, args: m.args[u]}) {
				reportUncollapsed(rep, u, "no parser rule restores "+string(u.Feature)+" at this path")
			}
		}
	}
	keep := shadowed(j, m)
	edited := map[ir.FeatureUse]bool{}
	for _, u := range m.uses {
		var vars []ir.Var
		edited[u] = (u.Feature == caps.JobEnv || u.Feature == caps.PipelineEnv) && decodeYAMLArg(m.args[u], &vars) && !envAsEmitted(j, vars)
	}
	collapseActions(rep, j, m)
	for _, u := range m.uses {
		if u.Feature != caps.JobEnv && u.Feature != caps.PipelineEnv {
			continue
		}
		var vars []ir.Var
		switch {
		case !decodeYAMLArg(m.args[u], &vars):
			reportUncollapsed(rep, u, "its argument is not a list of variables")
		case edited[u]:
			reportIgnored(rep, u, "the variable copy")
		case u.Feature == caps.JobEnv:
			j.Env = dropCopies(j, vars, keep)
		default:
			pipeEnv = dropCopies(j, vars, keep)
		}
	}
	return pipeEnv
}

func reportUncollapsed(rep *report.Report, u ir.FeatureUse, why string) {
	rep.Add(report.Entry{
		IRPath: u.IRPath, Feature: u.Feature, Level: caps.Emulated, Strategy: "kept as written",
		Detail: "marker not collapsed: " + why,
	})
}

// decodeArg reads the single JSON argument line of a marker.
func decodeArg(args []string, v any) bool {
	return len(args) == 1 && json.Unmarshal([]byte(args[0]), v) == nil
}

// decodeYAMLArg reads an argument that holds IR YAML text in a JSON string.
func decodeYAMLArg(args []string, v any) bool {
	var body string
	return decodeArg(args, &body) && yaml.Unmarshal([]byte(body), v) == nil
}

// collapseRefs turns the run-time variables that stand for secrets, variables and inputs back into references.
func collapseRefs(c *collapseCtx) bool {
	var stray axesArg
	if decodeArg(c.args, &stray) && len(stray.Axes) > 0 {
		return true
	}
	var a posArg
	if !decodeArg(c.args, &a) || !validPos(c.j, a.Pos) {
		return false
	}
	st := c.j.Steps[a.Pos]
	seen := map[string]int{}
	for _, ref := range a.Refs {
		ns, name, ok := strings.Cut(ref, ".")
		if !ok {
			return false
		}
		held := "${" + name + "}"
		for i, l := range st.Script {
			var sb strings.Builder
			for {
				k := strings.Index(l, held)
				if k < 0 {
					break
				}
				start, end := k, k+len(held)
				if slices.Contains(a.Q[name], seen[name]) && strings.HasSuffix(l[:start], vars.QuoteBreakOpen) && strings.HasPrefix(l[end:], vars.QuoteBreakClose) {
					start -= len(vars.QuoteBreakOpen)
					end += len(vars.QuoteBreakClose)
				}
				seen[name]++
				sb.WriteString(l[:start] + "${{ " + ref + " }}")
				l = l[end:]
			}
			sb.WriteString(l)
			st.Script[i] = sb.String()
		}
		if ns == expr.PrefixSecrets || ns == expr.PrefixVars {
			st.Env = slices.DeleteFunc(st.Env, func(v ir.Var) bool {
				return v.Name == name && v.Value.Kind == ir.ValueSecret && v.Value.Secret == name
			})
		}
	}
	return true
}

// collapseStepName gives a step with a made-up id its display name back: Woodpecker named it by that text.
func collapseStepName(c *collapseCtx) bool {
	var a posArg
	_, id, ok := strings.Cut(c.use.IRPath, stepSep)
	if !ok || !decodeArg(c.args, &a) || !validPos(c.j, a.Pos) {
		return false
	}
	wrote := a.Name
	if a.Wrote != "" {
		wrote = a.Wrote
	}
	if c.j.Steps[a.Pos].ID != wrote {
		return false
	}
	st := c.j.Steps[a.Pos]
	st.Name, st.ID = a.Name, id
	st.Ext = markDerived(st.Ext)
	return true
}

// collapseLostEnv puts back the variables of a step whose value had no Woodpecker spelling.
func collapseLostEnv(c *collapseCtx) bool {
	var a lostEnvArg
	var vars []ir.Var
	if !decodeArg(c.args, &a) || !validPos(c.j, a.Pos) ||
		yaml.Unmarshal([]byte(a.Vars), &vars) != nil || len(vars) != len(a.At) || slices.ContainsFunc(a.At, func(n int) bool { return n < 0 }) {
		return false
	}
	st := c.j.Steps[a.Pos]
	for i, v := range vars {
		st.Env = slices.Insert(st.Env, min(a.At[i], len(st.Env)), v)
	}
	return true
}

// validPos is true when pos names a step of j; a marker edited by hand can hold any number.
func validPos(j *ir.Job, pos int) bool { return pos >= 0 && pos < len(j.Steps) }

func markDerived(x ir.Ext) ir.Ext {
	if x == nil {
		x = ir.Ext{}
	}
	inner, _ := x[dialect].(map[string]any)
	if inner == nil {
		inner = map[string]any{}
		x[dialect] = inner
	}
	inner[ExtDerivedID] = true
	return x
}

// collapseContainer restores the job image. With an argument the job named it, and every step that took it
// loses it again; without one the steps ran in the default image only because the job named none.
func collapseContainer(c *collapseCtx) bool {
	ref := defaultImage
	if len(c.args) > 0 {
		var img ir.Image
		if !decodeArg(c.args, &img) {
			return false
		}
		c.j.Container, ref = &img, img.Ref
	}
	for _, st := range c.j.Steps {
		if st.Image != nil && st.Image.Ref == ref && len(st.Image.Entrypoint) == 0 {
			st.Image = nil
		}
	}
	return true
}

func collapseMatrix(c *collapseCtx) bool {
	var mx ir.Matrix
	if !decodeArg(c.args, &mx) {
		return false
	}
	if !matrixAsEmitted(c.j.Matrix, &mx) {
		reportIgnored(c.rep, c.use, "the matrix")
		return true
	}
	c.j.Matrix = &mx
	return true
}

// matrixAsEmitted is true when the parsed matrix lists exactly the combinations the emitter wrote for mx, which
// cuts the list at maxMatrixInclude.
func matrixAsEmitted(parsed, mx *ir.Matrix) bool {
	if parsed == nil || len(parsed.Axes) > 0 || len(parsed.Exclude) > 0 {
		return false
	}
	want := ir.Expand(mx)
	want = want[:min(len(want), maxMatrixInclude)]
	if len(want) != len(parsed.Include) {
		return false
	}
	for i := range want {
		if !sameCombo(want[i], parsed.Include[i]) {
			return false
		}
	}
	return true
}

func sameCombo(a, b ir.Combo) bool {
	if len(a) != len(b) {
		return false
	}
	for _, kv := range a {
		if !slices.Contains(b, kv) {
			return false
		}
	}
	return true
}

// reportIgnored records a marker that the file no longer matches, so the parsed value stands.
func reportIgnored(rep *report.Report, u ir.FeatureUse, what string) {
	rep.Add(report.Entry{
		IRPath: u.IRPath, Feature: u.Feature, Level: caps.Lost, Strategy: "marker ignored",
		Detail: "marker ignored: " + what + " was edited after conversion, so the parsed value stands",
	})
}

func collapseCache(c *collapseCtx) bool { return decodeArg(c.args, &c.j.Cache) }

func collapseArtifacts(c *collapseCtx) bool { return decodeArg(c.args, &c.j.Artifacts) }

// collapseAllowFailure moves failure: ignore from every step to the job; a step without it, bar the placeholder, means the file was edited.
func collapseAllowFailure(c *collapseCtx) bool {
	if slices.ContainsFunc(c.j.Steps, func(s *ir.Step) bool { return !s.ContinueOnError && s.ID != placeholderStep }) {
		reportIgnored(c.rep, c.use, "failure: ignore")
		return true
	}
	c.j.AllowFailure = true
	for _, st := range c.j.Steps {
		st.ContinueOnError = false
	}
	return true
}

// collapseNoop drops the step that stood in for a workflow with no runnable step.
func collapseNoop(c *collapseCtx) bool {
	if len(c.j.Steps) == 1 && c.j.Steps[0].ID == placeholderStep {
		c.j.Steps = nil
	}
	return true
}

// plainExt turns the nested maps the YAML decoder gives the type of the outer map back into plain maps, the
// form every parser gives Ext.
func plainExt(e ir.Ext) ir.Ext {
	if e == nil {
		return nil
	}
	out := make(ir.Ext, len(e))
	for k, v := range e {
		out[k] = plainAny(v)
	}
	return out
}

func plainAny(v any) any {
	switch t := v.(type) {
	case ir.Ext:
		return plainAny(map[string]any(t))
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = plainAny(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = plainAny(x)
		}
		return out
	}
	return v
}

// actionMark is one StepAction marker with its decoded argument.
type actionMark struct {
	use ir.FeatureUse
	arg actionArg
	st  ir.Step
}

// collapseActions puts back the action steps the emitter replaced with a clone setting, an image or the shared
// workspace, in their original order.
func collapseActions(rep *report.Report, j *ir.Job, m *marks) {
	var found []actionMark
	for _, u := range m.uses {
		if u.Feature != caps.StepAction {
			continue
		}
		am := actionMark{use: u}
		if !decodeArg(m.args[u], &am.arg) || am.arg.At < 0 || yaml.Unmarshal([]byte(am.arg.Step), &am.st) != nil {
			reportUncollapsed(rep, u, "its argument is not an action step")
			continue
		}
		am.st.Ext = plainExt(am.st.Ext)
		found = append(found, am)
	}
	slices.SortStableFunc(found, func(a, b actionMark) int { return a.arg.At - b.arg.At })
	for _, am := range found {
		st := am.st
		at := min(am.arg.At, len(j.Steps))
		if am.arg.Stand != "" {
			if at >= len(j.Steps) || !sameScript(strings.Join(j.Steps[at].Script, "\n"), am.arg.Stand) {
				reportIgnored(rep, am.use, "the script step was edited after pipemaster wrote it")
				continue
			}
			st.If = j.Steps[at].If
			j.Steps[at] = &st
			continue
		}
		if am.arg.Fail {
			if at >= len(j.Steps) || !emitkit.IsFailLoud(j.Steps[at].Script) {
				reportIgnored(rep, am.use, "the failing step was edited after pipemaster wrote it")
				continue
			}
			// The held step keeps no condition; the one the file carries is the one the source had.
			st.If = j.Steps[at].If
			j.Steps[at] = &st
			continue
		}
		if am.arg.Image != "" {
			for _, later := range j.Steps[at:] {
				if later.Image != nil && later.Image.Ref == am.arg.Image && len(later.Image.Entrypoint) == 0 {
					later.Image = nil
				}
			}
		}
		j.Steps = slices.Insert(j.Steps, at, &st)
	}
}

// dropCopies undoes the copy of job and pipeline variables into every step. The marker holds the variables, so
// each is restored exactly and removed from the steps that carry the same value.
func dropCopies(j *ir.Job, vars []ir.Var, keep map[*ir.Step][]string) []ir.Var {
	for _, st := range j.Steps {
		st.Env = slices.DeleteFunc(st.Env, func(v ir.Var) bool {
			return !slices.Contains(keep[st], v.Name) &&
				slices.ContainsFunc(vars, func(w ir.Var) bool { return w.Name == v.Name && reflect.DeepEqual(w.Value, v.Value) })
		})
	}
	return vars
}

// envAsEmitted is true when every command step still carries every variable of the marker; a step value that
// differs is the step's own override. An expression value can have no spelling, so the emitter may not have
// copied it.
func envAsEmitted(j *ir.Job, vars []ir.Var) bool {
	for _, st := range j.Steps {
		if len(st.Script) == 0 || st.ID == placeholderStep {
			continue
		}
		for _, v := range vars {
			if v.Value.Kind != ir.ValueExpr && !slices.ContainsFunc(st.Env, func(w ir.Var) bool { return w.Name == v.Name }) {
				return false
			}
		}
	}
	return true
}

// shadowed reads the markers of the variables that a step sets to the value it would inherit, by step place.
func shadowed(j *ir.Job, m *marks) map[*ir.Step][]string {
	out := map[*ir.Step][]string{}
	for _, u := range m.uses {
		var a shadowArg
		if u.Feature == caps.StepEnv && decodeArg(m.args[u], &a) && validPos(j, a.Pos) {
			out[j.Steps[a.Pos]] = append(out[j.Steps[a.Pos]], a.Names...)
		}
	}
	return out
}

// triggerRead is the pipeline trigger list of a project and whether every workflow agreed on it.
type triggerRead struct {
	list   []ir.Trigger
	shared bool
}

// representable keeps the triggers a Woodpecker file can hold.
func representable(ts []ir.Trigger) []ir.Trigger {
	var out []ir.Trigger
	for _, t := range ts {
		if _, ok := triggerEvents[t.Kind]; ok {
			out = append(out, t)
		}
	}
	return out
}

var triggerEvents = map[ir.TriggerKind]string{
	ir.TriggerPush: eventPush, ir.TriggerPR: eventPR, ir.TriggerTag: eventTag, ir.TriggerSchedule: eventCron, ir.TriggerManual: eventMan,
}

func kindsOf(ts []ir.Trigger) []ir.TriggerKind {
	out := make([]ir.TriggerKind, len(ts))
	for i, t := range ts {
		out[i] = t.Kind
	}
	return out
}

// markedWith returns the marker of the feature at the given path, if the file has one.
func (m *marks) markedWith(f caps.Feature, path string) (ir.FeatureUse, bool) {
	for _, u := range m.uses {
		if u.Feature == f && (path == "" || u.IRPath == path) {
			return u, true
		}
	}
	return ir.FeatureUse{}, false
}

// collapseTriggers reads the pipeline triggers. A manual workflow stands outside the shared list, and a marker
// that holds the original triggers replaces the list a file can only approximate.
func collapseTriggers(rep *report.Report, wfs []*workflow, specs []*whenSpec) triggerRead {
	var sel []*whenSpec
	var manual []*workflow
	for i, w := range wfs {
		if _, ok := w.marks.markedWith(caps.JobManual, ""); ok {
			manual = append(manual, w)
			continue
		}
		sel = append(sel, specs[i])
	}
	out := triggerRead{}
	if len(sel) > 0 {
		out.list = sharedTriggers(sel)
		out.shared = out.list != nil
	}
	if len(sel) > 0 && !out.shared {
		return out
	}
	for _, w := range wfs {
		for _, f := range []caps.Feature{caps.TriggerFilterBranch, caps.TriggerFilterPaths, caps.TriggerFilterTags} {
			if u, ok := w.marks.markedWith(f, pipelinePth); ok && len(sel) > 0 {
				out.list = restoreTriggers(rep, u, w.marks.args[u], out.list)
			}
		}
	}
	for _, w := range wfs {
		if u, ok := w.marks.markedWith(caps.TriggerManual, pipelinePth); ok && len(sel) > 0 {
			orig, aok := triggersArg(w.marks.args[u])
			if !aok || !isSubset(kindsOf(orig), kindsOf(out.list)) {
				reportUncollapsed(rep, u, "the file edit removed a trigger event of the original list")
				continue
			}
			out.list = representable(orig)
		}
	}
	for _, w := range manual {
		u, _ := w.marks.markedWith(caps.JobManual, "")
		if orig, ok := triggersArg(w.marks.args[u]); ok {
			out.list = representable(orig)
			out.shared = true
			break
		}
	}
	return out
}

// isSubset reports whether every kind of sub is in all.
func isSubset(sub, all []ir.TriggerKind) bool {
	return !slices.ContainsFunc(sub, func(k ir.TriggerKind) bool { return !slices.Contains(all, k) })
}

func triggersArg(args []string) ([]ir.Trigger, bool) {
	var ts []ir.Trigger
	return ts, decodeYAMLArg(args, &ts)
}

// restoreTriggers replaces the parsed list with the original when both name the same kinds in the same order.
func restoreTriggers(rep *report.Report, u ir.FeatureUse, args []string, parsed []ir.Trigger) []ir.Trigger {
	orig, ok := triggersArg(args)
	if !ok {
		reportUncollapsed(rep, u, "its argument is not a list of triggers")
		return parsed
	}
	orig = representable(orig)
	if !slices.Equal(kindsOf(orig), kindsOf(parsed)) {
		reportUncollapsed(rep, u, "the file edit changed the trigger events, so the original filters do not apply")
		return parsed
	}
	return orig
}

// collapsePipeline undoes the emulations that span workflows: a manual workflow and a finally workflow.
func collapsePipeline(rep *report.Report, pl *ir.Pipeline, wfs []*workflow) {
	var finally []*ir.Job
	for _, w := range wfs {
		if _, ok := w.marks.markedWith(caps.JobManual, ""); ok {
			w.job.Manual = true
			if isManualEvent(w.job.If) {
				w.job.If = nil
			}
		}
		if u, ok := w.marks.markedWith(caps.PipelineFinally, ""); ok {
			var a finallyArg
			if !decodeArg(w.marks.args[u], &a) {
				reportUncollapsed(rep, u, "its argument is not a finally record")
				continue
			}
			w.job.Needs = a.Needs
			if a.NoIf && isAlways(w.job.If) {
				w.job.If = nil
			}
			finally = append(finally, w.job)
		}
	}
	if len(finally) == 0 {
		return
	}
	pl.Jobs = slices.DeleteFunc(pl.Jobs, func(j *ir.Job) bool { return slices.Contains(finally, j) })
	pl.Finally = finally
}

// isManualEvent is true for the bare test that a manual workflow carries when the pipeline lists no manual trigger.
func isManualEvent(x expr.Expr) bool {
	b, ok := x.(expr.Binary)
	if !ok || b.Op != "==" {
		return false
	}
	r, rok := b.L.(expr.Ref)
	l, lok := b.R.(expr.Lit)
	return rok && lok && len(r.Path) == 1 && r.Path[0] == "event" && l.Str == eventMan
}

func isAlways(x expr.Expr) bool {
	c, ok := x.(expr.Call)
	return ok && c.Name == "always" && len(c.Args) == 0
}

// multiArg says which pipeline and job a workflow came from when an emitter folded several pipelines into one
// directory, and where each stood, because the file name holds neither.
type multiArg struct {
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
	P        int    `json:"p"`
	J        int    `json:"j"`
}

// group is the workflows that form one pipeline.
type group struct {
	id  string
	wfs []*workflow
}

// splitPipelines groups the workflows by the pipeline their marker names. A file without one belongs to the
// default pipeline, and a lone default group is the usual case.
func splitPipelines(wfs []*workflow) []*group {
	type placed struct {
		w *workflow
		a multiArg
	}
	byID := map[string][]placed{}
	var order []string
	pos := map[string]int{}
	for _, w := range wfs {
		a, ok := multiOf(w)
		id := pipelineID
		if ok {
			id = a.Pipeline
		}
		if _, seen := byID[id]; !seen {
			order = append(order, id)
			pos[id] = a.P
		}
		byID[id] = append(byID[id], placed{w, a})
	}
	slices.SortStableFunc(order, func(a, b string) int { return pos[a] - pos[b] })
	var out []*group
	for _, id := range order {
		ps := byID[id]
		slices.SortStableFunc(ps, func(a, b placed) int { return a.a.J - b.a.J })
		g := &group{id: id}
		for _, p := range ps {
			g.wfs = append(g.wfs, p.w)
		}
		out = append(out, g)
	}
	return out
}

func multiOf(w *workflow) (multiArg, bool) {
	u, ok := w.marks.markedWith(caps.ProjectMultiPipeline, "")
	var a multiArg
	return a, ok && decodeArg(w.marks.args[u], &a) && a.Pipeline != "" && a.Job != ""
}

// build assembles one pipeline from its workflows.
func (g *group) build(rep *report.Report) (*ir.Pipeline, error) {
	pl := &ir.Pipeline{ID: g.id}
	specs := make([]*whenSpec, len(g.wfs))
	files := make([]string, len(g.wfs))
	for i, w := range g.wfs {
		pl.Jobs = append(pl.Jobs, w.job)
		specs[i], files[i] = w.when, w.file
		if pl.Env == nil {
			pl.Env = w.pipeEnv
		}
	}
	for _, s := range specs {
		if s != nil {
			s.multi = len(g.wfs) > 1
		}
	}
	triggers := collapseTriggers(rep, g.wfs, specs)
	pl.Triggers = triggers.list
	for i, j := range pl.Jobs {
		if err := specs[i].apply(rep, j, files[i], pl.Triggers != nil); err != nil {
			return nil, fmt.Errorf("%s: %w", files[i], err)
		}
	}
	for _, w := range g.wfs {
		restoreChanges(rep, w)
		restoreJobIf(rep, w)
	}
	collapsePipeline(rep, pl, g.wfs)
	for _, w := range g.wfs {
		if _, empty := w.marks.markedWith(caps.StepScript, pipelinePth); empty {
			pl.Jobs = slices.DeleteFunc(pl.Jobs, func(j *ir.Job) bool { return j == w.job })
		}
	}
	sharedConcurrency(rep, pl, g.wfs, files)
	g.rename(rep, pl)
	return pl, nil
}

// rename gives the jobs of a folded pipeline their original IDs and points the report at them.
func (g *group) rename(rep *report.Report, pl *ir.Pipeline) {
	if g.id == pipelineID {
		return
	}
	ids := map[string]string{}
	for _, w := range g.wfs {
		if a, ok := multiOf(w); ok {
			ids[w.job.ID] = a.Job
		}
	}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		for i, n := range j.Needs {
			if id, ok := ids[n.JobID]; ok {
				j.Needs[i].JobID = id
			}
		}
	}
	for old, id := range ids {
		from, to := pipelinePth+"/job/"+old, "pipeline/"+g.id+"/job/"+id
		for i, e := range rep.Entries {
			if e.IRPath == from || strings.HasPrefix(e.IRPath, from+"/") {
				rep.Entries[i].IRPath = to + strings.TrimPrefix(e.IRPath, from)
			}
		}
	}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		if id, ok := ids[j.ID]; ok {
			j.ID = id
		}
	}
}

// collapseStepIf restores a step condition in its source dialect when the parsed one is still its translation.
func collapseStepIf(c *collapseCtx) bool {
	var a srcArg
	_, id, found := strings.Cut(c.use.IRPath, stepSep)
	if !found || !decodeArg(c.args, &a) {
		return false
	}
	from, ok := expr.LangOf(a.From)
	src, err := expr.ParseCanonical(a.Src)
	if !ok || err != nil {
		return false
	}
	t, ok := expr.TranslateFacts(src, *from, wpexpr.Lang, expr.Facts{Branch: a.Branch})
	if !ok {
		return false
	}
	for _, st := range c.j.Steps {
		if st.ID == id && st.If != nil && (sameCondition(st.If, t) || sameCondition(st.If, wpexpr.ReadIf(t))) {
			st.If = src
			return true
		}
	}
	return false
}

// restoreJobIf runs after the job condition is read from the when, which the job collapsers run too early for.
func restoreJobIf(rep *report.Report, w *workflow) {
	for _, u := range w.marks.uses {
		if u.Feature != caps.JobIf {
			continue
		}
		if !collapseJobIf(&collapseCtx{rep: rep, j: w.job, use: u, args: w.marks.args[u]}) {
			reportUncollapsed(rep, u, "no parser rule restores "+string(u.Feature)+" at this path")
		}
	}
}

// collapseJobIf restores the job condition in its source dialect when the parsed one is still its translation.
// A marker that holds no source only says where the condition sits, so there is nothing to restore.
func collapseJobIf(c *collapseCtx) bool {
	var a gateArg
	if !decodeArg(c.args, &a) {
		return false
	}
	if a.Src == "" {
		return true
	}
	from, ok := expr.LangOf(a.From)
	src, err := expr.ParseCanonical(a.Src)
	if !ok || err != nil || c.j.If == nil {
		return false
	}
	t, ok := expr.TranslateFacts(src, *from, wpexpr.Lang, expr.Facts{Branch: a.Branch})
	if !ok || (!sameCondition(c.j.If, t) && !sameCondition(c.j.If, wpexpr.ReadIf(t))) {
		return false
	}
	c.j.If = src
	return true
}

// sameCondition reports whether two conditions print as the same Woodpecker text: a !~ test is written, and read
// back, as the negation of a match.
func sameCondition(a, b expr.Expr) bool {
	ta, errA := wpexpr.Print(a)
	tb, errB := wpexpr.Print(b)
	if errA != nil || errB != nil {
		return expr.String(a) == expr.String(b)
	}
	return ta == tb
}

// restoreChanges puts the path predicate back as the job condition; the parser already took the filter the emitter
// wrote out of the triggers.
func restoreChanges(rep *report.Report, w *workflow) {
	for _, u := range w.marks.uses {
		var a gateArg
		if u.Feature != caps.JobIf || !decodeArg(w.marks.args[u], &a) || a.Paths == nil {
			continue
		}
		src, err := expr.ParseCanonical(a.Paths.Src)
		if err != nil || !expr.HasChanges(src) || w.job.If != nil {
			reportUncollapsed(rep, u, "the file edit changed the path filter, so the parsed one stands")
			continue
		}
		w.job.If = src
	}
}

// sameScript compares script text apart from the dollar escape and the final newline the file adds.
func sameScript(a, b string) bool {
	norm := func(s string) string { return strings.TrimRight(strings.ReplaceAll(s, "$$", "$"), "\n") }
	return norm(a) == norm(b)
}
