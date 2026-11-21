package drone

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	dexpr "github.com/lukaszraczylo/pipemaster/expr/drone"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const stepSep = "/step/"

var markerPipelineRE = regexp.MustCompile(`^pipeline/[^/]+`)

// marks are the emulation markers of one document, with their argument lines.
type marks struct {
	args map[ir.FeatureUse][]string
	// pipe is the pipeline ID the markers name; the parser builds one pipeline with a fixed ID, so only that prefix differs.
	pipe string
	uses []ir.FeatureUse
}

// readMarks collects the markers of a document. Argument lines escape "$" as "$$".
func readMarks(src string) *marks {
	m := &marks{args: map[ir.FeatureUse][]string{}}
	b := []byte(src)
	for _, u := range emitkit.ParseMarkers(b) {
		if m.pipe == "" {
			m.pipe = strings.TrimPrefix(markerPipelineRE.FindString(u.IRPath), "pipeline/")
		}
		u.IRPath = markerPipelineRE.ReplaceAllLiteralString(u.IRPath, pipelinePth)
		m.uses = append(m.uses, u)
	}
	for u, lines := range emitkit.ParseArgs(b) {
		u.IRPath = markerPipelineRE.ReplaceAllLiteralString(u.IRPath, pipelinePth)
		for _, l := range lines {
			m.args[u] = append(m.args[u], strings.ReplaceAll(l, dollarEsc, dollar))
		}
	}
	return m
}

// markedWith returns the marker of the feature at the given path, if the document has one; an empty path matches any.
func (m *marks) markedWith(f caps.Feature, path string) (ir.FeatureUse, bool) {
	for _, u := range m.uses {
		if u.Feature == f && (path == "" || u.IRPath == path) {
			return u, true
		}
	}
	return ir.FeatureUse{}, false
}

// collapseCtx is what a collapser works on: the document and the marker with its argument.
type collapseCtx struct {
	rep  *report.Report
	d    *doc
	j    *ir.Job
	use  ir.FeatureUse
	args []string
}

// collapser undoes one emulation and reports whether it found the construct the marker names.
type collapser func(c *collapseCtx) bool

// docCollapsers run in this order: a guard comes off before the text is read, and later ones read what earlier
// ones restore.
var docCollapsers = []struct {
	run     collapser
	feature caps.Feature
}{
	{collapseJobGuard, caps.JobIf},
	{collapseStepGuard, caps.StepIf},
	{collapseRefs, caps.ScriptInterpolation},
	{collapseContainer, caps.JobContainer},
	{collapseCache, caps.JobCache},
	{collapseArtifacts, caps.JobArtifacts},
	{collapseAllowFailure, caps.JobAllowFailure},
	{collapseNoop, caps.StepScript},
}

// collapseDoc undoes the emulations marked in one document. A marker it cannot restore is reported, so the
// construct stays visible as written.
func collapseDoc(d *doc) {
	j := d.job
	for _, cl := range docCollapsers {
		for _, u := range d.marks.uses {
			if u.Feature != cl.feature {
				continue
			}
			if !cl.run(&collapseCtx{rep: d.rep, d: d, j: j, use: u, args: d.marks.args[u]}) {
				reportUncollapsed(d.rep, u, "no parser rule restores "+string(u.Feature)+" at this path")
			}
		}
	}
	collapseEnv(d)
	d.written = slices.Clone(j.Steps)
	collapseActions(d)
}

func reportUncollapsed(rep *report.Report, u ir.FeatureUse, why string) {
	rep.Add(report.Entry{
		IRPath: u.IRPath, Feature: u.Feature, Level: caps.Emulated, Strategy: "kept as written",
		Detail: "marker not collapsed: " + why,
	})
}

// reportIgnored records a marker that the file no longer matches, so the parsed value stands.
func reportIgnored(rep *report.Report, u ir.FeatureUse, what string) {
	rep.Add(report.Entry{
		IRPath: u.IRPath, Feature: u.Feature, Level: caps.Lost, Strategy: "marker ignored",
		Detail: "marker ignored: " + what + " was edited after conversion, so the parsed value stands",
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

// validPos is true when pos names a step of j; a marker edited by hand can hold any number.
func validPos(j *ir.Job, pos int) bool { return pos >= 0 && pos < len(j.Steps) }

func isPlaceholder(st *ir.Step) bool {
	return st.ID == placeholderName && slices.Equal(st.Script, []string{"true"})
}

// dropGuardShape removes the command shape of a guard line, which the script no longer holds.
func dropGuardShape(st *ir.Step) {
	inner, _ := st.Ext[dialect].(map[string]any)
	shapes, _ := inner[ExtCommands].([]any)
	if len(shapes) == 0 {
		return
	}
	if len(shapes) > 1 {
		inner[ExtCommands] = shapes[1:]
		return
	}
	delete(inner, ExtCommands)
	if len(inner) == 0 {
		delete(st.Ext, dialect)
	}
	if len(st.Ext) == 0 {
		st.Ext = nil
	}
}

// collapseJobGuard takes the job condition back from the guard line that opens every script step.
func collapseJobGuard(c *collapseCtx) bool {
	if len(c.args) > 0 {
		return true
	}
	var cond expr.Expr
	var steps []*ir.Step
	for _, st := range c.j.Steps {
		if len(st.Script) > 0 && !isPlaceholder(st) {
			steps = append(steps, st)
		}
	}
	if len(steps) == 0 {
		return true
	}
	rests := make([][]string, len(steps))
	for i, st := range steps {
		rest, e, ok := dexpr.RemoveGuard(st.Script)
		if !ok || (cond != nil && expr.String(cond) != expr.String(e)) {
			reportIgnored(c.rep, c.use, "the job guard")
			return true
		}
		cond, rests[i] = e, rest
	}
	for i, st := range steps {
		st.Script = rests[i]
		dropGuardShape(st)
	}
	c.j.If = cond
	return true
}

// collapseStepGuard takes the step condition back from its guard line.
func collapseStepGuard(c *collapseCtx) bool {
	_, id, ok := strings.Cut(c.use.IRPath, stepSep)
	if !ok {
		return false
	}
	i := slices.IndexFunc(c.j.Steps, func(s *ir.Step) bool { return s.ID == id })
	if i < 0 {
		return false
	}
	st := c.j.Steps[i]
	if len(c.args) > 0 {
		return collapseStepCond(c, st)
	}
	rest, e, ok := dexpr.RemoveGuard(st.Script)
	if !ok {
		reportIgnored(c.rep, c.use, "the step guard")
		return true
	}
	st.Script, st.If = rest, e
	dropGuardShape(st)
	return true
}

// collapseStepCond gives a step the condition that the marker holds when the file still reads as the marker says.
func collapseStepCond(c *collapseCtx, st *ir.Step) bool {
	var a condArg
	if !decodeArg(c.args, &a) {
		return false
	}
	orig, err := expr.ParseCanonical(a.If)
	if err != nil {
		return false
	}
	if st.If == nil || expr.String(st.If) != a.Back {
		reportIgnored(c.rep, c.use, "the step condition")
		return true
	}
	st.If = orig
	return true
}

// collapseRefs turns the run-time variables that stand for secrets, variables and inputs back into references.
func collapseRefs(c *collapseCtx) bool {
	var a posArg
	if !decodeArg(c.args, &a) || !validPos(c.j, a.Pos) {
		return false
	}
	st := c.j.Steps[a.Pos]
	for _, ref := range a.Refs {
		ns, name, ok := strings.Cut(ref, ".")
		if !ok {
			return false
		}
		held := "${" + name + "}"
		if !slices.ContainsFunc(st.Script, func(l string) bool { return strings.Contains(l, held) }) {
			reportIgnored(c.rep, c.use, "the reference "+ref)
			continue
		}
		for i, l := range st.Script {
			st.Script[i] = strings.ReplaceAll(l, held, "${{ "+ref+" }}")
		}
		if ns == expr.PrefixSecrets || ns == expr.PrefixVars {
			st.Env = slices.DeleteFunc(st.Env, func(v ir.Var) bool {
				return v.Name == name && v.Value.Kind == ir.ValueSecret && v.Value.Secret == name
			})
		}
	}
	return true
}

// collapseContainer restores the job image. With an argument the job named it, and every step that took it
// loses it again; without one the steps ran in the default image only because the job named none.
func collapseContainer(c *collapseCtx) bool {
	ref := defaultImage
	if len(c.args) > 0 {
		var a containerArg
		if !decodeArg(c.args, &a) {
			return false
		}
		held := 0
		for _, st := range c.j.Steps {
			if st.Image != nil && st.Image.Ref == a.Ref {
				held++
			}
		}
		if held < a.Taken {
			reportIgnored(c.rep, c.use, "the job image")
			return true
		}
		img := a.Image
		c.j.Container, ref = &img, img.Ref
	}
	for _, st := range c.j.Steps {
		if st.Image != nil && st.Image.Ref == ref && len(st.Image.Entrypoint) == 0 {
			st.Image = nil
		}
	}
	return true
}

func collapseCache(c *collapseCtx) bool { return decodeArg(c.args, &c.j.Cache) }

func collapseArtifacts(c *collapseCtx) bool { return decodeArg(c.args, &c.j.Artifacts) }

// collapseAllowFailure moves failure: ignore from every step to the job; a step without it, bar the placeholder, means the file was edited.
func collapseAllowFailure(c *collapseCtx) bool {
	if slices.ContainsFunc(c.j.Steps, func(s *ir.Step) bool { return !s.ContinueOnError && !isPlaceholder(s) }) {
		reportIgnored(c.rep, c.use, "failure: ignore")
		return true
	}
	c.j.AllowFailure = true
	for _, st := range c.j.Steps {
		st.ContinueOnError = false
	}
	return true
}

// collapseNoop drops the step that stood in for a job with no runnable step.
func collapseNoop(c *collapseCtx) bool {
	if len(c.j.Steps) == 1 && isPlaceholder(c.j.Steps[0]) {
		c.j.Steps = nil
		c.d.noop = true
	}
	return true
}

// collapseEnv undoes the copy of job and pipeline variables: the job marker holds the variables of the job, the
// pipeline marker those of the pipeline, and secrets sit on every step.
func collapseEnv(d *doc) {
	j := d.job
	var jvars, pvars []ir.Var
	ju, hasJ := d.marks.markedWith(caps.JobEnv, "")
	pu, hasP := d.marks.markedWith(caps.PipelineEnv, "")
	if hasJ && !decodeYAMLArg(d.marks.args[ju], &jvars) {
		reportUncollapsed(d.rep, ju, "its argument is not a list of variables")
		hasJ = false
	}
	if hasP && !decodeYAMLArg(d.marks.args[pu], &pvars) {
		reportUncollapsed(d.rep, pu, "its argument is not a list of variables")
		hasP = false
	}
	if !hasJ && !hasP {
		return
	}
	if !envAsEmitted(j, append(slices.Clone(jvars), pvars...)) {
		for _, u := range []struct {
			use ir.FeatureUse
			ok  bool
		}{{ju, hasJ}, {pu, hasP}} {
			if u.ok {
				reportIgnored(d.rep, u.use, "the variable copy")
			}
		}
		return
	}
	all := append(slices.Clone(jvars), pvars...)
	for _, st := range j.Steps {
		st.Env = slices.DeleteFunc(st.Env, func(v ir.Var) bool {
			return v.Value.Kind == ir.ValueSecret && slices.ContainsFunc(all, func(w ir.Var) bool { return w.Name == v.Name && reflect.DeepEqual(w.Value, v.Value) })
		})
	}
	if hasJ {
		j.Env = jvars
	} else {
		j.Env = slices.DeleteFunc(j.Env, func(v ir.Var) bool {
			return slices.ContainsFunc(pvars, func(w ir.Var) bool { return w.Name == v.Name && reflect.DeepEqual(w.Value, v.Value) })
		})
	}
	if hasP {
		d.pipeEnv = pvars
	}
}

// envAsEmitted is true when every command step still carries each secret of the marker and the document
// environment holds each other variable; a variable that is gone means the file was edited. An expression value
// can have no spelling, so the emitter may not have copied it.
func envAsEmitted(j *ir.Job, vars []ir.Var) bool {
	for _, st := range j.Steps {
		if len(st.Script) == 0 || isPlaceholder(st) {
			continue
		}
		for _, v := range vars {
			if v.Value.Kind == ir.ValueSecret && !slices.ContainsFunc(st.Env, func(w ir.Var) bool { return w.Name == v.Name }) {
				return false
			}
		}
	}
	for _, v := range vars {
		if v.Value.Kind == ir.ValueLit && !slices.ContainsFunc(j.Env, func(w ir.Var) bool { return w.Name == v.Name }) {
			return false
		}
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
func collapseActions(d *doc) {
	var found []actionMark
	for _, u := range d.marks.uses {
		if u.Feature != caps.StepAction {
			continue
		}
		am := actionMark{use: u}
		if !decodeArg(d.marks.args[u], &am.arg) || am.arg.At < 0 || yaml.Unmarshal([]byte(am.arg.Step), &am.st) != nil {
			reportUncollapsed(d.rep, u, "its argument is not an action step")
			continue
		}
		am.st.Ext = plainExt(am.st.Ext)
		found = append(found, am)
	}
	slices.SortStableFunc(found, func(a, b actionMark) int { return a.arg.At - b.arg.At })
	j := d.job
	for inserted, am := range found {
		st := am.st
		at := min(am.arg.At+inserted, len(j.Steps))
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
