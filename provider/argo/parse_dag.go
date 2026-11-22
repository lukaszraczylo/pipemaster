package argo

import (
	"fmt"
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Template type keys; a template has one of them.
const (
	kindDag          = "dag"
	kindSteps        = "steps"
	kindContainer    = "container"
	kindScript       = "script"
	kindContainerSet = "containerSet"
	kindSuspend      = "suspend"
)

var templateKinds = []string{kindContainer, kindScript, kindDag, kindSteps, "resource", kindSuspend, "http", "plugin", kindContainerSet, "data"}

// templateKind finds the type key of a template.
func templateKind(t *omap) (string, bool) {
	for _, k := range templateKinds {
		if _, ok := t.Vals[k]; ok {
			return k, true
		}
	}
	return "", false
}

// call is one task of a dag or one step of a steps template.
type call struct {
	m    *omap
	name string
	id   string
	// prev lists the Argo names of the previous group, for a step.
	prev []string
	gate bool
}

// orchestrate reads the template that starts the workflow or the exit handler into jobs. A dag template gives a
// job per task and a steps template a job per step. A template of any other type is one job.
func (c *plCtx) orchestrate(name string, finally bool, prefix string) ([]*ir.Job, error) {
	t := c.src.tmpls[name]
	c.src.used[name] = true
	kind, _ := templateKind(t)
	r := newReader(t)
	r.Take("name")
	var calls []*call
	var err error
	switch kind {
	case kindDag:
		calls, err = c.dagCalls(r)
	case kindSteps:
		calls, err = c.stepCalls(r)
	default:
		return c.single(name, finally)
	}
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	c.k.KeepRestAs(r, prefix)
	jobs, err := c.callJobs(calls, finally)
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	return c.foldGates(calls, jobs), nil
}

// single reads a template that is no dag or steps as the one job of the workflow.
func (c *plCtx) single(name string, finally bool) ([]*ir.Job, error) {
	m := ynode.NewMap()
	m.Set("name", scalOf(name), 0)
	m.Set("template", scalOf(name), 0)
	cl := &call{m: m, name: name, id: name}
	jobs, err := c.callJobs([]*call{cl}, finally)
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	return jobs, nil
}

func (c *plCtx) callID(m *omap, name string) string {
	if id := m.Marks[""]; id != "" {
		return id
	}
	if id := m.Marks["name"]; id != "" {
		return id
	}
	return name
}

func (c *plCtx) dagCalls(r *reader) ([]*call, error) {
	dv, _ := r.Take(kindDag)
	dm, ok := asMap(dv)
	if !ok {
		return nil, fmt.Errorf("dag must be a mapping")
	}
	dr := newReader(dm)
	tv, _ := dr.Take("tasks")
	c.k.KeepRestAs(dr, "dag_")
	return c.calls(tv, "tasks", nil)
}

func (c *plCtx) stepCalls(r *reader) ([]*call, error) {
	sv, _ := r.Take(kindSteps)
	groups, ok := sv.([]any)
	if !ok {
		return nil, fmt.Errorf("steps must be a list")
	}
	var out []*call
	var prev []string
	for i, g := range groups {
		list := g
		if _, isList := g.([]any); !isList {
			list = []any{g}
		}
		cs, err := c.calls(list, fmt.Sprintf("steps group %d", i+1), prev)
		if err != nil {
			return nil, err
		}
		prev = prev[:0:0]
		for _, cl := range cs {
			prev = append(prev, cl.name)
		}
		out = append(out, cs...)
	}
	return out, nil
}

// calls reads a list of tasks or the steps of one group.
func (c *plCtx) calls(v any, what string, prev []string) ([]*call, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", what)
	}
	var out []*call
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return nil, fmt.Errorf("%s entry %d must be a mapping", what, i+1)
		}
		name, _ := text(m.Vals["name"])
		if name == "" {
			return nil, fmt.Errorf("%s entry %d needs a name", what, i+1)
		}
		out = append(out, &call{m: m, name: name, id: c.callID(m, name), prev: prev})
	}
	return out, nil
}

// callJobs builds the job of each call.
func (c *plCtx) callJobs(calls []*call, finally bool) ([]*ir.Job, error) {
	ids := map[string]string{}
	for _, cl := range calls {
		if _, dup := ids[cl.name]; dup {
			return nil, fmt.Errorf("task %q is defined twice", cl.name)
		}
		ids[cl.name] = cl.id
	}
	var jobs []*ir.Job
	for _, cl := range calls {
		cl.gate = c.isGate(cl)
		b := c.newJob(cl, ids, finally)
		if err := b.build(); err != nil {
			return nil, fmt.Errorf("task %s: %w", cl.name, err)
		}
		jobs = append(jobs, b.job)
	}
	return jobs, nil
}

// isGate tells a task that only runs a bare suspend template: it has no other key than its name, template and
// dependencies or depends, and the template holds an empty suspend and nothing else.
func (c *plCtx) isGate(cl *call) bool {
	tn, ok := text(cl.m.Vals["template"])
	if !ok {
		return false
	}
	for _, k := range cl.m.Order {
		if k != "name" && k != "template" && k != "dependencies" && k != "depends" {
			return false
		}
	}
	t := c.src.tmpls[tn]
	if t == nil {
		return false
	}
	s, isMap := asMap(t.Vals[kindSuspend])
	return len(t.Order) == 2 && isMap && len(s.Order) == 0
}

// foldGates removes each gate that jobs wait for and marks those jobs Manual. A gate that a condition reads, or
// that nothing waits for, stays a job.
func (c *plCtx) foldGates(calls []*call, jobs []*ir.Job) []*ir.Job {
	gates := map[string]*call{}
	for _, cl := range calls {
		if cl.gate {
			gates[cl.id] = cl
		}
	}
	if len(gates) == 0 {
		return jobs
	}
	dependents := map[string]int{}
	read := map[string]bool{}
	for _, j := range jobs {
		for _, n := range j.Needs {
			dependents[n.JobID]++
		}
		expr.NeedRefs(j.If, func(id string) { read[id] = true })
	}
	fold := map[string]*ir.Job{}
	for _, j := range jobs {
		if _, ok := gates[j.ID]; ok && dependents[j.ID] > 0 && !read[j.ID] {
			fold[j.ID] = j
		}
	}
	if len(fold) == 0 {
		return jobs
	}
	var out []*ir.Job
	for _, j := range jobs {
		if fold[j.ID] != nil {
			continue
		}
		var needs []ir.Need
		var seen []any
		for _, n := range j.Needs {
			if fold[n.JobID] == nil {
				needs = appendNeed(needs, n)
				continue
			}
			j.Manual = true
			for _, g := range expandGate(n.JobID, fold) {
				seen = append(seen, c.gateExt(gates[g]))
				j.If = andExpr(fold[g].If, j.If)
			}
			for _, dn := range gateNeeds(n.JobID, fold) {
				needs = appendNeed(needs, dn)
			}
			c.p.rep.Add(gateEntry(c, j, gates[n.JobID]))
		}
		j.Needs = needs
		if j.Manual {
			c.gateMarker(j)
		}
		if len(seen) > 0 {
			if j.Ext == nil {
				j.Ext = ir.Ext{dialect: map[string]any{}}
			}
			e := j.Ext[dialect].(map[string]any)
			list, _ := e[ExtGates].([]any)
			e[ExtGates] = append(list, seen...)
		}
		out = append(out, j)
	}
	return out
}

func appendNeed(needs []ir.Need, n ir.Need) []ir.Need {
	i := slices.IndexFunc(needs, func(x ir.Need) bool { return x.JobID == n.JobID })
	if i < 0 {
		return append(needs, n)
	}
	needs[i].Artifacts = needs[i].Artifacts || n.Artifacts
	return needs
}

// expandGate lists the folded gates that a wait on id passes through, outermost first.
func expandGate(id string, fold map[string]*ir.Job) []string {
	out := []string{id}
	for _, n := range fold[id].Needs {
		if fold[n.JobID] != nil {
			out = append(out, expandGate(n.JobID, fold)...)
		}
	}
	return out
}

// gateNeeds lists the needs of the gate chain that are no gate.
func gateNeeds(id string, fold map[string]*ir.Job) []ir.Need {
	var out []ir.Need
	for _, n := range fold[id].Needs {
		if fold[n.JobID] != nil {
			out = append(out, gateNeeds(n.JobID, fold)...)
			continue
		}
		out = append(out, n)
	}
	return out
}

func (c *plCtx) gateExt(cl *call) any {
	tn, _ := text(cl.m.Vals["template"])
	return map[string]any{"task": cl.name, "template": tn, "body": plain(c.src.tmpls[tn])}
}

func gateEntry(c *plCtx, j *ir.Job, cl *call) report.Entry {
	return report.Entry{
		IRPath: c.k.Path + "/job/" + j.ID, Feature: caps.JobManual, Level: caps.Lost, Strategy: c.k.Strategy(), SourceLoc: c.k.Loc(cl.m.Lines["name"]),
		Detail: "task " + cl.name + " is a suspend template; job " + j.ID + " is manual and the suspend task is kept in Ext",
	}
}
