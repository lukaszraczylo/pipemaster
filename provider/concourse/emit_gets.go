package concourse

import (
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
)

// getKeys is the order of the keys of a get step.
var getKeys = []string{"get", "resource", "passed", "trigger", "params", "version"}

// extGet is a source get that a Concourse file held, with the resource it names.
type extGet struct {
	m    map[string]any
	name string
}

// ownGets lists the source gets that the job Ext holds, in order.
func (jx *jobEmitter) ownGets() []extGet {
	var out []extGet
	l, _ := jx.ext[ExtGets].([]any)
	for _, raw := range l {
		m, ok := deepCopy(raw).(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["resource"].(string)
		if name == "" {
			name, _ = m["get"].(string)
		}
		if name != "" {
			out = append(out, extGet{m: m, name: name})
		}
	}
	return out
}

// jobTriggers returns the names of the resources that start the job: its own set when a Concourse file gave one,
// none for a manual job, the passed holders for a job with needs, and the pipeline triggers for the others.
func (jx *jobEmitter) jobTriggers(holders []string) map[string]bool {
	pe := jx.pe
	set := map[string]bool{}
	if names, ok := jx.ext[ExtTriggers].([]any); ok && !jx.gated() {
		for _, v := range names {
			if n, _ := v.(string); pe.byName[n] != nil {
				set[n] = true
			}
		}
		return set
	}
	if jx.manual {
		return set
	}
	if len(jx.j.Needs) > 0 {
		for _, h := range holders {
			set[h] = true
		}
		return set
	}
	for _, s := range pe.sources {
		if s.trigger {
			set[s.name] = true
		}
	}
	return set
}

// gated is true for a job that a person must approve, which no trigger set overrides.
func (jx *jobEmitter) gated() bool {
	return jx.j.Environment != nil && jx.j.Environment.Approval
}

// gets writes the source gets of the job: every git resource, and each other resource that starts the job.
func (jx *jobEmitter) gets() []*yaml.Node {
	pe, j, e := jx.pe, jx.j, jx.pe.e
	if jx.j.Manual && jx.ext[ExtTriggers] != nil && !jx.gated() {
		jx.manual = false
	}
	own := jx.ownGets()
	if l, ok := jx.ext[ExtGets].([]any); ok && len(l) == 0 {
		jx.held = true
		return nil
	}
	var holders []string
	for _, g := range own {
		if l, ok := g.m["passed"].([]any); ok && len(l) > 0 {
			holders = append(holders, g.name)
		}
	}
	if len(holders) == 0 {
		holders = []string{pe.primary.name}
	}
	trig := jx.jobTriggers(holders)
	var names []string
	for _, g := range own {
		if !slices.Contains(names, g.name) {
			names = append(names, g.name)
		}
	}
	_, fileGets := jx.ext[ExtGets].([]any)
	for _, s := range pe.sources {
		if (s.typ == typeGit || trig[s.name]) && !slices.Contains(names, s.name) && !fileGets {
			names = append(names, s.name)
		}
	}
	jx.held = len(trig) == 0
	var passed []any
	for _, n := range j.Needs {
		if w, ok := pe.jobName[n.JobID]; ok {
			passed = append(passed, w)
		}
	}
	var nodes []*yaml.Node
	for _, name := range names {
		src := pe.byName[name]
		if src == nil {
			src = pe.adopt(name)
		}
		if src == nil {
			e.lose(jx.path, caps.ExtPassthrough, "the get of "+name+" names no resource; it is not written")
			continue
		}
		src.used = true
		var m map[string]any
		for _, g := range own {
			if g.name == name {
				m = g.m
			}
		}
		if m == nil {
			m = map[string]any{"get": name}
		}
		delete(m, "passed")
		delete(m, "trigger")
		if len(passed) > 0 && slices.Contains(holders, name) {
			m["passed"] = passed
		}
		if trig[name] {
			m["trigger"] = true
		}
		if p := jx.checkoutParams(); p != nil && name == pe.primary.name && m["params"] == nil {
			m["params"] = p
		}
		jx.taken[name] = true
		nodes = append(nodes, orderedNode(m, getKeys, nil))
	}
	if len(trig) == 0 && !jx.manual && len(j.Needs) == 0 && len(pe.sources) > 0 && !jx.hasTriggerSource() {
		e.emulate(jx.path, caps.TriggerManual, "get with no trigger", "no trigger starts the job, so a person starts it")
	}
	if jx.ext[ExtGetsParallel] == true && len(nodes) > 0 {
		ip := newMap()
		ip.Set("in_parallel", seqOf(nodes...))
		return []*yaml.Node{ip.N}
	}
	return nodes
}

func (jx *jobEmitter) hasTriggerSource() bool {
	for _, s := range jx.pe.sources {
		if s.trigger {
			return true
		}
	}
	return false
}

// adopt adds a resource of a Concourse-origin pipeline that a get names but no trigger made.
func (pe *pipeEmitter) adopt(name string) *source {
	def := pe.extDef(name)
	if def == nil {
		return nil
	}
	typ, _ := def[keyType].(string)
	return pe.add(&source{name: name, typ: typ, def: def})
}
