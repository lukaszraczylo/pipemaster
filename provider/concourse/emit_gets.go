package concourse

import (
	"fmt"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
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
	noGets := false
	var fresh map[string][]any
	if l, ok := jx.ext[ExtGets].([]any); ok && len(l) == 0 {
		missing, nested := jx.needsNotInNestedGets()
		if len(missing) == 0 {
			jx.held = len(jx.jobTriggers(nil)) == 0
			return nil
		}
		var add map[string][]any
		add, fresh = jx.placeNeeds(missing, nested)
		jx.nestedExtra = add
		if len(fresh) == 0 {
			jx.held = len(jx.jobTriggers(nil)) == 0
			return nil
		}
		if len(nested) > 0 {
			jx.held = len(jx.jobTriggers(nil)) == 0
			return jx.freshGets(fresh)
		}
		noGets = true
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
	if noGets {
		// A passed list lives on a get, so a need on a job that had none adds a get of a resource the needed job uses.
		names = sortedFresh(fresh, pe.primary.name)
	}
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
		fileOrder, _ := m["passed"].([]any)
		delete(m, "passed")
		delete(m, "trigger")
		pl := passed
		if noGets {
			pl = fresh[name]
		}
		if len(pl) > 0 && (noGets || slices.Contains(holders, name)) {
			m["passed"] = pl
			if sameSet(fileOrder, pl) {
				m["passed"] = fileOrder
			}
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

// needsNotInNestedGets lists the needs that no passed list of a get nested in a step of the plan names, and the
// resources of those nested gets. The nested lists stay as the file has them. A get in a hook is no need to the
// parser, so only the steps of the main plan count.
func (jx *jobEmitter) needsNotInNestedGets() ([]ir.Need, []string) {
	held := map[string]bool{}
	var nested []string
	main, _ := jx.split()
	for _, st := range main {
		nestedGets(extOf(st.Ext), held, &nested)
	}
	var out []ir.Need
	for _, n := range jx.j.Needs {
		if w, ok := jx.pe.jobName[n.JobID]; ok && !held[w] {
			out = append(out, n)
		}
	}
	return out, nested
}

// getResource is the resource that a get step names.
func getResource(m map[string]any) string {
	if r, _ := m["resource"].(string); r != "" {
		return r
	}
	n, _ := m["get"].(string)
	return n
}

// nestedGets adds the names of every passed list of a get in the verbatim step values to held, and the resource of
// every such get to res, in document order.
func nestedGets(v any, held map[string]bool, res *[]string) {
	switch t := v.(type) {
	case map[string]any:
		if t["get"] != nil {
			if r := getResource(t); r != "" && !slices.Contains(*res, r) {
				*res = append(*res, r)
			}
			if l, ok := t["passed"].([]any); ok {
				for _, x := range l {
					if n, ok := x.(string); ok {
						held[n] = true
					}
				}
			}
		}
		for _, k := range sortedKeys(t) {
			nestedGets(t[k], held, res)
		}
	case []any:
		for _, x := range t {
			nestedGets(x, held, res)
		}
	}
}

// jobResources is the resources that a job gets: its own gets and the gets nested in its steps when a Concourse
// file gave them, else every git resource and each resource that triggers.
func (pe *pipeEmitter) jobResources(o *ir.Job) map[string]bool {
	out := map[string]bool{}
	if ext := extOf(o.Ext); pe.e.own && ext != nil {
		if l, ok := ext[ExtGets].([]any); ok {
			for _, raw := range l {
				if m, ok := raw.(map[string]any); ok {
					out[getResource(m)] = true
				}
			}
			var nested []string
			for _, st := range o.Steps {
				nestedGets(extOf(st.Ext), map[string]bool{}, &nested)
			}
			for _, r := range nested {
				out[r] = true
			}
			return out
		}
	}
	for _, s := range pe.sources {
		if s.typ == typeGit || s.trigger {
			out[s.name] = true
		}
	}
	return out
}

// placeNeeds decides where each need goes that no nested passed list names. Passed is an AND over the versions of
// one resource, so a need joins the passed list of a nested get of a resource that the needed job also gets. Else it
// goes on a new get of a resource that the needed job gets and this job does not. Else no passed list can order the
// two jobs, and the edge is reported as lost.
func (jx *jobEmitter) placeNeeds(missing []ir.Need, nested []string) (add, fresh map[string][]any) {
	pe := jx.pe
	add, fresh = map[string][]any{}, map[string][]any{}
	// A get in a hook shares the name scope of the job, so fly rejects a second get of that resource.
	var inHooks []string
	_, hooks := jx.split()
	for _, st := range hooks {
		nestedGets(extOf(st.Ext), map[string]bool{}, &inHooks)
	}
	for _, n := range missing {
		w := pe.jobName[n.JobID]
		var rc map[string]bool
		for _, o := range pe.pl.Jobs {
			if o.ID == n.JobID {
				rc = pe.jobResources(o)
			}
		}
		if rc == nil {
			for _, o := range pe.pl.Finally {
				if o.ID == n.JobID {
					rc = pe.jobResources(o)
				}
			}
		}
		if i := slices.IndexFunc(nested, func(r string) bool { return rc[r] }); i >= 0 {
			add[nested[i]] = append(add[nested[i]], w)
			continue
		}
		var pick string
		for _, r := range sortedKeys(rc) {
			if pe.byName[r] != nil && !slices.Contains(nested, r) && !slices.Contains(inHooks, r) && (pick == "" || r == pe.primary.name) {
				pick = r
			}
		}
		if pick == "" {
			jx.pe.e.lose(jx.path, caps.JobNeeds, "the job "+w+" gets no resource that this job can also get, so no passed list orders the two; the need is not written")
			continue
		}
		fresh[pick] = append(fresh[pick], w)
	}
	return add, fresh
}

// sortedFresh lists the resources of the new gets, the shared source first.
func sortedFresh(fresh map[string][]any, primary string) []string {
	out := sortedKeys(fresh)
	if i := slices.Index(out, primary); i > 0 {
		out = append([]string{primary}, slices.Delete(out, i, i+1)...)
	}
	return out
}

// freshGets writes a get with a passed list for each resource of fresh, beside the gets nested in the plan.
func (jx *jobEmitter) freshGets(fresh map[string][]any) []*yaml.Node {
	var nodes []*yaml.Node
	for _, r := range sortedFresh(fresh, jx.pe.primary.name) {
		jx.pe.byName[r].used = true
		jx.taken[r] = true
		nodes = append(nodes, orderedNode(map[string]any{"get": r, "passed": fresh[r]}, getKeys, nil))
	}
	return nodes
}

// addNestedPassed puts each need that joins a nested get into the passed list of the first get of its resource in a
// copy of v.
func (jx *jobEmitter) addNestedPassed(v any) any {
	if len(jx.nestedExtra) == 0 {
		return v
	}
	c := deepCopy(v)
	for _, r := range sortedKeys(jx.nestedExtra) {
		if jx.extendFirstPassed(c, r, jx.nestedExtra[r]) {
			delete(jx.nestedExtra, r)
		}
	}
	return c
}

func (jx *jobEmitter) extendFirstPassed(v any, res string, names []any) bool {
	switch t := v.(type) {
	case map[string]any:
		if t["get"] != nil && getResource(t) == res {
			l, _ := t["passed"].([]any)
			t["passed"] = append(l, names...)
			return true
		}
		for _, k := range sortedKeys(t) {
			if jx.extendFirstPassed(t[k], res, names) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if jx.extendFirstPassed(x, res, names) {
				return true
			}
		}
	}
	return false
}

// reportUnplacedNeeds reports the needs that joined a nested get no step wrote.
func (jx *jobEmitter) reportUnplacedNeeds() {
	for _, r := range sortedKeys(jx.nestedExtra) {
		jx.pe.e.lose(jx.path, caps.JobNeeds, fmt.Sprintf("the needs %v belong to a get of %s, and no step of the plan wrote one; they are not written", jx.nestedExtra[r], r))
	}
	jx.nestedExtra = nil
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

// sameSet is true when both lists hold the same names, whatever their order; the IR keeps needs as a set, and the
// file order is the one a reader expects to see again.
func sameSet(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !slices.Contains(b, v) {
			return false
		}
	}
	return true
}
