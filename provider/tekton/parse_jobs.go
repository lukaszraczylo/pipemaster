package tekton

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
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
	for _, m := range append(append([]*omap{}, regular...), fin...) {
		name, ok := text(m.vals["name"])
		if !ok || name == "" {
			return fmt.Errorf("a task needs a name")
		}
		if _, dup := c.ids[name]; dup {
			return fmt.Errorf("task %q is defined twice", name)
		}
		id := m.marks[""]
		if id == "" {
			id = m.marks["name"]
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
	ws           *workspaceArg
	containerRef string
	path         string
	finally      bool
}

func (c *plCtx) job(m *omap, finally bool) (*ir.Job, error) {
	name, _ := text(m.vals["name"])
	id := c.ids[name]
	b := &jobBuilder{
		c: c, job: &ir.Job{ID: id}, finally: finally, explicit: map[string]bool{}, whenNeeds: map[string]bool{}, raw: map[string]string{},
		path: "pipeline/" + c.plID + "/job/" + id,
		px:   newTexts(c.taskID, false), tx: newTexts(c.taskID, true),
	}
	b.k = newSink(c.p.rep, c.file, b.path)
	b.ws = c.p.workspaceOf(m, b.path)
	c.ws = c.ws || b.ws != nil
	r := newReader(m)
	r.take("name")
	if err := b.header(r); err != nil {
		return nil, fmt.Errorf("task %s: %w", name, err)
	}
	if err := b.taskBody(r); err != nil {
		return nil, fmt.Errorf("task %s: %w", name, err)
	}
	b.k.keepRest(r)
	b.finish()
	return b.job, nil
}

// header reads the PipelineTask keys that map to job fields.
func (b *jobBuilder) header(r *reader) error {
	j := b.job
	if t, ok := textOf(r.take("displayName")); ok {
		j.Name = t
	}
	if v, ok := r.take("description"); ok {
		b.k.vals["description"] = plain(v)
	}
	if v, ok := r.take("runAfter"); ok {
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
	if v, ok := r.take("timeout"); ok {
		if d, ok := parseDuration(v); ok {
			j.Timeout = &d
		} else {
			b.k.keep("timeout", v, caps.JobTimeout, "timeout has no positive Go duration; kept in Ext", r.line("timeout"))
		}
	}
	if v, ok := r.take("onError"); ok {
		if t, _ := text(v); t == "continue" {
			j.AllowFailure = true
		} else {
			b.k.keep("onError", v, caps.JobAllowFailure, "onError other than continue has no IR form; kept in Ext", r.line("onError"))
		}
	}
	if v, ok := r.take("workspaces"); ok && b.ws == nil {
		b.k.keep("workspaces", v, caps.ExtPassthrough, "task workspaces have no IR field; kept in Ext", r.line("workspaces"))
	}
	if v, ok := r.take("matrix"); ok {
		if err := b.matrix(v, r.line("matrix")); err != nil {
			b.k.keep("matrix", v, caps.JobMatrix, "matrix has no IR form ("+err.Error()+"); kept in Ext", r.line("matrix"))
		}
	}
	if v, ok := r.take("params"); ok {
		if err := b.bindings(v); err != nil {
			return err
		}
	}
	if v, ok := r.take("when"); ok {
		cond, err := b.when(v, r.line("when"))
		if err != nil {
			return err
		}
		j.If = cond
	}
	return nil
}

func (b *jobBuilder) retries(r *reader) {
	v, ok := r.take("retries")
	if !ok {
		return
	}
	t, _ := text(v)
	n, err := strconv.Atoi(t)
	switch {
	case err != nil || n < 0:
		b.k.keep("retries", v, caps.JobRetry, "retries is not a count; kept in Ext", r.line("retries"))
	case n > 0:
		b.job.Retry = &ir.Retry{Attempts: n + 1}
	}
}

func parseDuration(v any) (ir.Duration, bool) {
	t, ok := text(v)
	if !ok {
		return 0, false
	}
	d, err := time.ParseDuration(t)
	if err != nil || d <= 0 {
		return 0, false
	}
	return ir.Duration(d), true
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
	if pv, ok := r.take("params"); ok {
		list, _ := pv.([]any)
		for _, e := range list {
			em, _ := asMap(e)
			name, _ := text(valueOf(em, "name"))
			vals, ok := stringList(valueOf(em, "value"))
			if name == "" || !ok {
				return fmt.Errorf("matrix.params needs a name and a list of strings")
			}
			mx.Axes = append(mx.Axes, ir.Axis{Name: name, Values: vals})
			names[name] = true
		}
	}
	var incNames []any
	named := false
	if iv, ok := r.take("include"); ok {
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
	if len(r.rest()) > 0 {
		return fmt.Errorf("matrix holds keys beside params and include")
	}
	for n := range names {
		b.tx.matrix[n] = true
	}
	if named {
		b.k.vals["matrix_include_names"] = incNames
		b.k.note(caps.JobMatrix, "matrix.include names have no IR field; kept in Ext", line)
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
	other := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return fmt.Errorf("a param needs a name")
		}
		val, isText := text(m.vals["value"])
		if isText {
			b.tx.bind[name] = b.px.parts(val)
			b.raw[name] = val
			continue
		}
		other.set(name, m.vals["value"], m.lines["value"])
		b.k.note(caps.ExtPassthrough, "param "+name+" is not a string; kept in Ext", m.lines["value"])
	}
	b.rawParams = v
	if len(other.keys) > 0 {
		b.k.vals["array_params"] = plain(other)
	}
	return nil
}

// finish adds the needs that references imply, the kept-variable entries and the Ext.
func (b *jobBuilder) finish() {
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
		b.job.Needs = append(b.job.Needs, ir.Need{JobID: id})
		list = append(list, id)
	}
	if len(list) > 0 {
		b.k.vals[ExtImplicitNeeds] = list
	}
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
	b.job.Ext = b.k.ext()
}

// stringList reads a YAML sequence of scalars. A bare scalar is no list: it may be a reference to an array param.
func stringList(v any) ([]string, bool) {
	if _, isSeq := v.([]any); !isSeq {
		return nil, false
	}
	return textList(v)
}
