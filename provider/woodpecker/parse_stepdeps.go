package woodpecker

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Ext keys of the step DAG. ExtDependsOn holds the source depends_on of a step, which the Woodpecker emitter
// writes back. ExtPos holds the file position of a step that the parser moved to keep the dependency order.
const (
	keyDependsOn = "depends_on"
	ExtDependsOn = "_depends_on"
	ExtPos       = "_pos"
)

// stepDep is the dependency list of one step and the line it was read from.
type stepDep struct {
	// optional holds the names a {name, optional: true} entry marks; such a step can be absent.
	optional map[string]bool
	names    []string
	line     int
}

// readStepDeps reads a step depends_on: a name, or a list of names and {name, optional} entries.
func readStepDeps(v any, line int) (stepDep, error) {
	d := stepDep{line: line, optional: map[string]bool{}}
	if t, ok := text(v); ok {
		d.names = []string{t}
		return d, nil
	}
	list, ok := v.([]any)
	if !ok {
		return d, fmt.Errorf("line %d: depends_on must be a name or a list of names", line)
	}
	for _, e := range list {
		if t, ok := text(e); ok {
			d.names = append(d.names, t)
			continue
		}
		m, isMap := asMap(e)
		if !isMap {
			return d, fmt.Errorf("line %d: depends_on entries must be names or {name, optional} mappings", line)
		}
		n, ok := text(m.vals["name"])
		if !ok {
			return d, fmt.Errorf("line %d: a depends_on entry needs a name", line)
		}
		d.names = append(d.names, n)
		if o, _ := text(m.vals["optional"]); o == "true" {
			d.optional[n] = true
		}
	}
	return d, nil
}

// stepIndex maps each step ID, and the map key of a map-form step, to its position.
func stepIndex(steps []*ir.Step) map[string]int {
	at := map[string]int{}
	for i, st := range steps {
		at[st.ID] = i
	}
	for i, st := range steps {
		if mk, _ := extOf(st.Ext)[ExtMapKey].(string); mk != "" {
			if _, taken := at[mk]; !taken {
				at[mk] = i
			}
		}
	}
	return at
}

// topoOrder lists the step positions so that each step follows its dependencies. Ready steps keep file order.
// strict makes a name that no step has an error; otherwise such a dependency is skipped. A cycle returns the
// IDs of the steps it blocks.
func topoOrder(steps []*ir.Step, deps map[string]stepDep, strict bool) ([]int, []string, error) {
	at := stepIndex(steps)
	need := make([][]int, len(steps))
	for i, st := range steps {
		d, ok := deps[st.ID]
		if !ok {
			continue
		}
		for _, n := range d.names {
			j, known := at[n]
			switch {
			case known:
				need[i] = append(need[i], j)
			case strict && !d.optional[n]:
				return nil, nil, fmt.Errorf("line %d: step %s: depends_on names the step %q, which does not exist", d.line, st.ID, n)
			}
		}
	}
	done := make([]bool, len(steps))
	order := make([]int, 0, len(steps))
	for len(order) < len(steps) {
		next := -1
		for i := range steps {
			if !done[i] && !slices.ContainsFunc(need[i], func(j int) bool { return !done[j] }) {
				next = i
				break
			}
		}
		if next < 0 {
			var stuck []string
			for i, st := range steps {
				if !done[i] {
					stuck = append(stuck, st.ID)
				}
			}
			return nil, stuck, nil
		}
		done[next] = true
		order = append(order, next)
	}
	return order, nil, nil
}

// checkStepDeps fails a workflow whose step depends_on names an unknown step or forms a cycle.
func (w *wf) checkStepDeps(j *ir.Job) error {
	if len(w.deps) == 0 {
		return nil
	}
	_, stuck, err := topoOrder(j.Steps, w.deps, true)
	if err != nil {
		return err
	}
	if len(stuck) > 0 {
		line := 0
		for _, id := range stuck {
			if d, ok := w.deps[id]; ok {
				line = d.line
				break
			}
		}
		return fmt.Errorf("line %d: depends_on forms a cycle among the steps %s", line, strings.Join(stuck, ", "))
	}
	return nil
}

// orderSteps moves each step behind its dependencies and reports the emulation. Steps run one after another,
// so independent steps do not run in parallel, and a failed step stops the steps after it. The original file
// position goes to Ext so that the Woodpecker emitter restores the source order.
func (w *wf) orderSteps(j *ir.Job) {
	if len(w.deps) == 0 {
		return
	}
	order, stuck, _ := topoOrder(j.Steps, w.deps, false)
	if len(stuck) > 0 {
		return
	}
	for _, st := range j.Steps {
		d, ok := w.deps[st.ID]
		if !ok {
			continue
		}
		w.rep.Add(report.Entry{
			IRPath: w.jobPath() + "/step/" + st.ID, Feature: caps.ExtPassthrough, Level: caps.Emulated, Strategy: "steps ordered by depends_on",
			Detail:    "the steps are sorted so each follows its depends_on; they run one after another, so independent steps do not run in parallel and a failed step stops later independent steps",
			SourceLoc: fmt.Sprintf("%s:%d", w.file, d.line),
		})
	}
	if slices.IsSorted(order) {
		return
	}
	out := make([]*ir.Step, len(order))
	for to, from := range order {
		st := j.Steps[from]
		if st.Ext == nil {
			st.Ext = ir.Ext{}
		}
		m, _ := st.Ext[dialect].(map[string]any)
		if m == nil {
			m = map[string]any{}
			st.Ext[dialect] = m
		}
		m[ExtPos] = from
		out[to] = st
	}
	j.Steps = out
}

// sourceOrder returns the steps in their file order when every step carries its position.
func sourceOrder(steps []*ir.Step) []*ir.Step {
	pos := make([]int, len(steps))
	for i, st := range steps {
		if st == nil {
			return steps
		}
		switch p := extOf(st.Ext)[ExtPos].(type) {
		case int:
			pos[i] = p
		case float64:
			pos[i] = int(p)
		default:
			return steps
		}
	}
	idx := make([]int, len(steps))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return pos[a] - pos[b] })
	out := make([]*ir.Step, len(steps))
	for to, from := range idx {
		out[to] = steps[from]
	}
	return out
}
