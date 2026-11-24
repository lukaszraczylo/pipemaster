package tekton

import (
	"fmt"
	"github.com/lukaszraczylo/pipemaster/actions"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

// jobs reads the tasks and the finally tasks into jobs.
func (c *plCtx) jobs(pl *ir.Pipeline, tasks, finally any) error {
	regular, err := c.taskList(tasks, "tasks")
	if err != nil {
		return err
	}
	fin, err := c.taskList(finally, "finally")
	if err != nil {
		return err
	}
	regular = withoutChangesTask(regular)
	c.srcWS = c.sourceWorkspaces(append(append([]*omap{}, regular...), fin...))
	for _, m := range append(append([]*omap{}, regular...), fin...) {
		name, ok := text(m.Vals["name"])
		if !ok || name == "" {
			return fmt.Errorf("a task needs a name")
		}
		if _, dup := c.ids[name]; dup {
			return fmt.Errorf("task %q is defined twice", name)
		}
		id := m.Marks[""]
		if id == "" {
			id = m.Marks["name"]
		}
		if id == "" {
			id = name
		}
		c.ids[name] = id
	}
	for _, grp := range []struct {
		dst     *[]*ir.Job
		src     []*omap
		finally bool
	}{{&pl.Jobs, regular, false}, {&pl.Finally, fin, true}} {
		for _, m := range grp.src {
			j, err := c.job(m, grp.finally)
			if err != nil {
				return err
			}
			*grp.dst = append(*grp.dst, j)
		}
	}
	return nil
}

func (c *plCtx) taskList(v any, what string) ([]*omap, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", what)
	}
	var out []*omap
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return nil, fmt.Errorf("%s entry %d must be a mapping", what, i+1)
		}
		out = append(out, m)
	}
	return out, nil
}

// jobBuilder reads one PipelineTask and its Task body.
type jobBuilder struct {
	rawParams    any
	whenNeeds    map[string]bool
	job          *ir.Job
	px           *texts
	tx           *texts
	explicit     map[string]bool
	c            *plCtx
	k            *sink
	raw          map[string]string
	opaque       map[string]bool
	prints       map[string]string
	ws           *workspaceArg
	marks        *marks
	containerRef string
	path         string
	finally      bool
	inTask       bool
}

func (c *plCtx) job(m *omap, finally bool) (*ir.Job, error) {
	name, _ := text(m.Vals["name"])
	id := c.ids[name]
	b := &jobBuilder{
		c: c, job: &ir.Job{ID: id}, finally: finally, explicit: map[string]bool{}, whenNeeds: map[string]bool{}, raw: map[string]string{}, opaque: map[string]bool{},
		path: "pipeline/" + c.plID + "/job/" + id,
		px:   newTexts(c.taskID, false), tx: newTexts(c.taskID, true),
	}
	b.k = newSink(c.p.rep, c.file, b.path)
	b.ws = c.p.workspaceOf(m, b.path)
	b.tx.srcWS = c.taskSourceWorkspaces(m)
	c.ws = c.ws || b.ws != nil
	r := newReader(m)
	r.Take("name")
	if err := b.header(r); err != nil {
		return nil, fmt.Errorf("task %s: %w", name, err)
	}
	if err := b.taskBody(r); err != nil {
		return nil, fmt.Errorf("task %s: %w", name, err)
	}
	b.addSourceCheckout()
	b.k.KeepRest(r)
	b.finish()
	return b.job, nil
}

// header reads the PipelineTask keys that map to job fields.
func (b *jobBuilder) header(r *reader) error {
	j := b.job
	if t, ok := textOf(r.Take("displayName")); ok {
		j.Name = t
	}
	if v, ok := r.Take("description"); ok {
		b.k.Out["description"] = plain(v)
	}
	if v, ok := r.Take("runAfter"); ok {
		names, ok := textList(v)
		if !ok {
			return fmt.Errorf("runAfter must be a list of task names")
		}
		for _, n := range names {
			id, known := b.c.taskID(n)
			if !known {
				return fmt.Errorf("runAfter names the unknown task %q", n)
			}
			b.explicit[n] = true
			j.Needs = append(j.Needs, ir.Need{JobID: id})
		}
	}
	b.retries(r)
	if v, ok := r.Take("timeout"); ok {
		if d, ok := parseDuration(v); ok {
			j.Timeout = &d
		} else {
			b.k.Keep("timeout", v, caps.JobTimeout, "timeout has no positive Go duration; kept in Ext", r.Line("timeout"))
		}
	}
	if v, ok := r.Take("onError"); ok {
		if t, _ := text(v); t == "continue" {
			j.AllowFailure = true
		} else {
			b.k.Keep("onError", v, caps.JobAllowFailure, "onError other than continue has no IR form; kept in Ext", r.Line("onError"))
		}
	}
	if v, ok := r.Take("workspaces"); ok && b.ws == nil {
		b.k.Keep("workspaces", v, caps.ExtPassthrough, "task workspaces have no IR field; kept in Ext", r.Line("workspaces"))
	}
	if v, ok := r.Take("matrix"); ok {
		if err := b.matrix(v, r.Line("matrix")); err != nil {
			b.k.Keep("matrix", v, caps.JobMatrix, "matrix has no IR form ("+err.Error()+"); kept in Ext", r.Line("matrix"))
		}
	}
	if v, ok := r.Take("params"); ok {
		if err := b.bindings(v); err != nil {
			return err
		}
	}
	if v, ok := r.Take("when"); ok {
		cond, err := b.when(v, r.Line("when"))
		if err != nil {
			return err
		}
		j.If = cond
	}
	return nil
}

func (b *jobBuilder) retries(r *reader) {
	v, ok := r.Take("retries")
	if !ok {
		return
	}
	t, _ := text(v)
	n, err := strconv.Atoi(t)
	switch {
	case err != nil || n < 0:
		b.k.Keep("retries", v, caps.JobRetry, "retries is not a count; kept in Ext", r.Line("retries"))
	case n > 0:
		b.job.Retry = &ir.Retry{Attempts: n + 1}
	}
}

// matrix maps matrix.params to axes and matrix.include to include combinations.
func (b *jobBuilder) matrix(v any, line int) error {
	mm, ok := asMap(v)
	if !ok {
		return fmt.Errorf("matrix is not a mapping")
	}
	r := newReader(mm)
	mx := &ir.Matrix{}
	names := map[string]bool{}
	if pv, ok := r.Take("params"); ok {
		list, _ := pv.([]any)
		for _, e := range list {
			em, _ := asMap(e)
			name, _ := text(valueOf(em, "name"))
			vals, ok := stringList(valueOf(em, "value"))
			if name == "" || !ok {
				return fmt.Errorf("matrix.params needs a name and a list of strings")
			}
			if len(vals) == 0 {
				b.k.Note(caps.JobMatrix, "matrix param "+name+" has no values, so the matrix runs no task", line)
			}
			mx.Axes = append(mx.Axes, ir.Axis{Name: name, Values: vals})
			names[name] = true
		}
	}
	var incNames []any
	named := false
	if iv, ok := r.Take("include"); ok {
		list, _ := iv.([]any)
		for _, e := range list {
			em, _ := asMap(e)
			name, _ := text(valueOf(em, "name"))
			incNames = append(incNames, name)
			named = named || name != ""
			var combo ir.Combo
			ps, _ := valueOf(em, "params").([]any)
			for _, pe := range ps {
				pm, _ := asMap(pe)
				k, _ := text(valueOf(pm, "name"))
				val, ok := text(valueOf(pm, "value"))
				if k == "" || !ok {
					return fmt.Errorf("matrix.include params need a name and a string value")
				}
				combo = append(combo, ir.KV{Key: k, Value: val})
				names[k] = true
			}
			mx.Include = append(mx.Include, combo)
		}
	}
	if len(r.Rest()) > 0 {
		return fmt.Errorf("matrix holds keys beside params and include")
	}
	for n := range names {
		b.tx.matrix[n] = true
	}
	if named {
		b.k.Out["matrix_include_names"] = incNames
		b.k.Note(caps.JobMatrix, "matrix.include names have no IR field; kept in Ext", line)
	}
	b.job.Matrix = mx
	return nil
}

// bindings reads the params that the PipelineTask passes to its Task. A string value binds the Task param;
// any other value stays in Ext.
func (b *jobBuilder) bindings(v any) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("params must be a list")
	}
	other := ynode.NewMap()
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return fmt.Errorf("a param needs a name")
		}
		val, isText := text(m.Vals["value"])
		if isText && b.hasUnresolved(val) {
			// The Task text keeps $(params.name), so the Task must keep its param and the PipelineTask its binding.
			b.opaque[name] = true
			other.Set(name, m.Vals["value"], m.Lines["value"])
			b.k.Note(caps.ExtPassthrough, "param "+name+" binds "+val+", a Tekton variable with no IR form; the binding and the Task param are kept in Ext", m.Lines["value"])
			continue
		}
		if isText {
			b.tx.bind[name] = b.px.parts(val)
			b.raw[name] = val
			continue
		}
		other.Set(name, m.Vals["value"], m.Lines["value"])
		b.k.Note(caps.ExtPassthrough, "param "+name+" is not a string; kept in Ext", m.Lines["value"])
	}
	b.rawParams = v
	if len(other.Order) > 0 {
		b.k.Out["array_params"] = plain(other)
	}
	return nil
}

// hasUnresolved tells a bound value that holds a Tekton variable which has no IR reference.
func (b *jobBuilder) hasUnresolved(val string) bool {
	probe := newTexts(b.c.taskID, false)
	probe.parts(val)
	return len(probe.kept) > 0
}

// finish adds the needs that references imply, the kept-variable entries and the Ext.
func (b *jobBuilder) finish() {
	b.collapse()
	names := map[string]bool{}
	for _, t := range []*texts{b.px, b.tx} {
		for n := range t.needs {
			names[n] = true
		}
	}
	for n := range b.whenNeeds {
		names[n] = true
	}
	var implicit []string
	for n := range names {
		if b.explicit[n] {
			continue
		}
		if id, ok := b.c.taskID(n); ok && id != b.job.ID {
			implicit = append(implicit, id)
		}
	}
	sort.Strings(implicit)
	var list []any
	for _, id := range implicit {
		if !slices.ContainsFunc(b.job.Needs, func(n ir.Need) bool { return n.JobID == id }) {
			b.job.Needs = append(b.job.Needs, ir.Need{JobID: id})
		}
		list = append(list, id)
	}
	if len(list) > 0 {
		b.k.Out[ExtImplicitNeeds] = list
	}
	b.legacyParams()
	b.boundParams()
	seen := map[string]bool{}
	for _, t := range []*texts{b.px, b.tx} {
		for _, f := range t.kept {
			if seen[f] {
				continue
			}
			seen[f] = true
			b.c.p.rep.Add(report.Entry{
				IRPath: b.path, Feature: caps.ScriptInterpolation, Level: caps.Lost, Strategy: "kept as written",
				Detail: f + " is a Tekton variable with no IR reference; it stays in the text as written", SourceLoc: b.c.file,
			})
		}
	}
	if b.ws != nil {
		b.restoreWorkspace()
	}
	b.job.Ext = b.k.Ext()
}

// legacyParams keeps the names of the Task params that the text reads only as $(inputs.params.x), so the Tekton
// file gets that spelling back. A param read in both spellings is written as $(params.x).
func (b *jobBuilder) legacyParams() {
	var only []any
	for _, name := range slices.Sorted(maps.Keys(b.tx.legacy)) {
		if b.tx.modern[name] {
			b.k.Note(caps.ScriptInterpolation, "param "+name+" is read as $(inputs.params."+name+") and as $(params."+name+"); the Tekton file writes $(params."+name+")", 0)
			continue
		}
		only = append(only, name)
	}
	if len(only) > 0 {
		b.k.Out[ExtLegacyParams] = only
	}
}

// boundParams keeps the PipelineTask value of each Task param that the Task text reads under another name than
// the pipeline param, which is the name the emitter picks without it.
func (b *jobBuilder) boundParams() {
	if _, catalog := b.k.Out[ExtCatalogRef]; catalog {
		return
	}
	bound := map[string]any{}
	for name := range b.tx.used {
		if raw, ok := b.raw[name]; ok && isPipelineParam(raw) && raw != paramPrefix+name+paramClose {
			bound[name] = raw
		}
	}
	if len(bound) > 0 {
		b.k.Out[ExtBoundParams] = bound
	}
}

// isPipelineParam tells a value that is exactly one pipeline param variable.
func isPipelineParam(v string) bool {
	rest, ok := strings.CutPrefix(v, paramPrefix)
	return ok && strings.HasSuffix(rest, paramClose) && !strings.ContainsAny(strings.TrimSuffix(rest, paramClose), "()$")
}

// workspaceBindings lists the (task workspace, pipeline workspace) pairs of a PipelineTask.
func workspaceBindings(pt *omap) [][2]string {
	var out [][2]string
	list, _ := pt.Vals["workspaces"].([]any)
	for _, e := range list {
		m := asMapOrNil(e)
		name, _ := text(valueOf(m, "name"))
		ws, _ := text(valueOf(m, "workspace"))
		if name != "" && ws != "" {
			out = append(out, [2]string{name, ws})
		}
	}
	return out
}

// sourceWorkspaces finds the pipeline workspaces that a catalog git-clone task writes the clone to.
func (c *plCtx) sourceWorkspaces(tasks []*omap) map[string]bool {
	out := map[string]bool{}
	for _, m := range tasks {
		rm := asMapOrNil(m.Vals["taskRef"])
		if rm == nil {
			continue
		}
		name, _ := text(rm.Vals["name"])
		_, resolver := rm.Vals["resolver"]
		_, bundle := rm.Vals["bundle"]
		if _, local := c.p.tasks[name]; local && !resolver && !bundle {
			continue
		}
		if t, ok, err := catalogEntry(rm); err == nil && ok && t.isCheckout {
			for _, b := range workspaceBindings(m) {
				out[b[1]] = true
			}
		}
	}
	return out
}

// taskSourceWorkspaces names the Task workspaces that a PipelineTask binds to a source workspace.
func (c *plCtx) taskSourceWorkspaces(pt *omap) map[string]bool {
	out := map[string]bool{}
	for _, b := range workspaceBindings(pt) {
		if c.srcWS[b[1]] {
			out[b[0]] = true
		}
	}
	return out
}

// addSourceCheckout gives a job whose Task reads the source workspace its own checkout step. The workspace that
// the clone filled is not shared between the jobs of a target with one runner per job. The Tekton emitter skips
// the step, because the clone task of the pipeline already fills the workspace.
func (b *jobBuilder) addSourceCheckout() {
	if len(b.tx.srcWS) == 0 || b.k.Out[ExtCatalogRef] != nil || len(b.job.Steps) == 0 {
		return
	}
	for _, st := range b.job.Steps {
		if st.Kind == ir.StepTask {
			return
		}
	}
	ref, ok := actions.Reverse(actionCheckout)
	if !ok {
		return
	}
	id := b.job.ID + "-checkout"
	st := &ir.Step{ID: id, Kind: ir.StepAction, Action: &ref, Ext: ir.Ext{dialect: map[string]any{ExtSourceCheckout: true}}}
	b.job.Steps = append([]*ir.Step{st}, b.job.Steps...)
	b.c.p.rep.Add(report.Entry{
		IRPath: b.path, Feature: caps.StepAction, Level: caps.Emulated, Strategy: checkoutStrategy,
		Detail: "the Task reads the workspace that the git-clone task filled, which is not shared between jobs, so the job clones the pipeline repository itself", SourceLoc: b.c.file,
	})
}
