package woodpecker

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const platformLabel = "platform"

// workflow is a parsed file: its job, and the when that Parse places once it knows every workflow.
type workflow struct {
	job   *ir.Job
	when  *whenSpec
	marks *marks
	conc  *concurrency
	file  string
	// pipeEnv holds the pipeline variables an emitter copied into the steps of this file.
	pipeEnv []ir.Var
}

// wf is the state of one workflow parse.
type wf struct {
	tx  *texts
	rep *report.Report
	ext *sink
	// deps holds the depends_on of the steps, by step ID.
	deps map[string]stepDep
	file string
	id   string
}

func (w *wf) jobPath() string { return pipelinePth + "/job/" + w.id }

func parseWorkflow(rep *report.Report, file string, data []byte) (*workflow, error) {
	doc, skipped, err := parseDocument(data)
	if err != nil {
		return nil, err
	}
	for _, line := range skipped {
		rep.Add(report.Entry{
			IRPath: pipelinePth + "/job/" + workflowName(file), Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "dropped",
			Detail:    "a merge key (<<) whose value is not a mapping adds no key and is dropped",
			SourceLoc: fmt.Sprintf("%s:%d", file, line),
		})
	}
	top, ok := asMap(doc)
	if !ok {
		return nil, fmt.Errorf("a workflow must be a mapping")
	}
	r := newReader(top)
	w := &wf{tx: &texts{axes: map[string]bool{}}, rep: rep, file: file, id: workflowName(file)}
	w.ext = newSink(rep, file, w.jobPath())
	j := &ir.Job{ID: w.id}
	m := readMarks(data)
	w.tx.markAxes(m)
	if err := w.matrix(j, r); err != nil {
		return nil, err
	}
	stepsNode, ok := r.take("steps")
	if !ok {
		return nil, fmt.Errorf("steps is required")
	}
	if err := w.steps(j, stepsNode); err != nil {
		return nil, err
	}
	if err := w.checkStepDeps(j); err != nil {
		return nil, err
	}
	if v, ok := r.take("services"); ok {
		if err := w.services(j, v); err != nil {
			return nil, err
		}
	}
	w.labels(j, r)
	if err := w.needs(j, r); err != nil {
		return nil, err
	}
	conc := w.concurrency(r)
	spec, err := w.when(r)
	if err != nil {
		return nil, err
	}
	w.ext.keepRest(r, map[string]caps.Feature{"concurrency": caps.PipelineConcurrency})
	spec.configure(m)
	pipeEnv := collapseJob(rep, j, m)
	w.orderSteps(j)
	w.noteKept()
	w.noteRuntimeVars()
	j.Ext = w.ext.ext()
	return &workflow{job: j, when: spec, pipeEnv: pipeEnv, marks: m, conc: conc}, nil
}

// matrix maps the matrix. Woodpecker reads only include when it is present, so sibling axes are kept, not read.
func (w *wf) matrix(j *ir.Job, r *reader) error {
	v, ok := r.take("matrix")
	if !ok {
		return nil
	}
	m, isMap := asMap(v)
	if !isMap {
		return fmt.Errorf("line %d: matrix must be a mapping", r.line("matrix"))
	}
	var mx ir.Matrix
	ignored := &omap{vals: map[string]any{}, lines: map[string]int{}}
	_, hasInclude := m.vals["include"]
	for _, k := range m.keys {
		switch {
		case k == "include":
			inc, err := w.combos(m.vals[k], m.lines[k])
			if err != nil {
				return err
			}
			mx.Include = inc
			for _, c := range inc {
				for _, kv := range c {
					w.tx.axes[kv.Key] = true
				}
			}
		case hasInclude:
			ignored.set(k, m.vals[k], m.lines[k])
		default:
			vals, ok := textList(m.vals[k])
			if !ok {
				return fmt.Errorf("line %d: matrix axis %s must be a list of scalars", m.lines[k], k)
			}
			mx.Axes = append(mx.Axes, ir.Axis{Name: k, Values: unescapeAll(vals)})
			w.tx.axes[k] = true
		}
	}
	if len(ignored.keys) > 0 {
		w.ext.keep("matrix", ignored, caps.JobMatrix, "matrix axes next to include are ignored by Woodpecker; kept in Ext", r.line("matrix"))
	}
	j.Matrix = &mx
	return nil
}

func (w *wf) combos(v any, line int) ([]ir.Combo, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("line %d: matrix include must be a list", line)
	}
	var out []ir.Combo
	for _, e := range list {
		m, ok := asMap(e)
		if !ok {
			return nil, fmt.Errorf("line %d: a matrix include entry must be a mapping", line)
		}
		var c ir.Combo
		for _, k := range m.keys {
			t, ok := text(m.vals[k])
			if !ok {
				return nil, fmt.Errorf("line %d: matrix include value %s must be a scalar", m.lines[k], k)
			}
			c = append(c, ir.KV{Key: k, Value: unescape(t)})
		}
		out = append(out, c)
	}
	return out, nil
}

// labels maps labels.platform to the runner label; other labels have no IR field.
func (w *wf) labels(j *ir.Job, r *reader) {
	v, ok := r.take("labels")
	if !ok {
		return
	}
	m, isMap := asMap(v)
	if !isMap {
		r.release("labels")
		return
	}
	rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for _, k := range m.keys {
		if t, ok := text(m.vals[k]); ok && k == platformLabel {
			if lbl, err := w.tx.template(t); err == nil {
				j.Runner.Labels = append(j.Runner.Labels, lbl)
				continue
			}
		}
		rest.set(k, m.vals[k], m.lines[k])
	}
	if len(rest.keys) == 0 {
		return
	}
	w.ext.vals["labels"] = plain(rest)
	for _, k := range rest.keys {
		w.ext.note(caps.JobRunner, "labels."+k+" has no IR field; kept in Ext", rest.lines[k])
	}
}

// needs maps workflow depends_on to job needs.
func (w *wf) needs(j *ir.Job, r *reader) error {
	v, ok := r.take("depends_on")
	if !ok {
		return nil
	}
	ns, err := dependencies(v)
	if err != nil {
		return fmt.Errorf("line %d: depends_on: %w", r.line("depends_on"), err)
	}
	for _, n := range ns {
		j.Needs = append(j.Needs, ir.Need{JobID: n.name, Optional: n.optional})
	}
	return nil
}

type dep struct {
	name     string
	optional bool
}

// dependencies reads a depends_on value: a name, or a list of names and {name, optional} entries.
func dependencies(v any) ([]dep, error) {
	if t, ok := text(v); ok {
		return []dep{{name: t}}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a name or a list")
	}
	var out []dep
	for _, e := range list {
		if t, ok := text(e); ok {
			out = append(out, dep{name: t})
			continue
		}
		m, ok := asMap(e)
		if !ok {
			return nil, fmt.Errorf("an entry must be a name or a mapping")
		}
		name, ok := text(m.vals["name"])
		if !ok {
			return nil, fmt.Errorf("an entry needs a name")
		}
		var opt bool
		if ov, has := m.vals["optional"]; has {
			if opt, ok = boolOf(ov); !ok {
				return nil, fmt.Errorf("optional of %s must be a boolean", name)
			}
		}
		for _, k := range m.keys {
			if k != "name" && k != "optional" {
				return nil, fmt.Errorf("an entry has the unknown key %s", k)
			}
		}
		out = append(out, dep{name: name, optional: opt})
	}
	return out, nil
}

// noteKept reports each ${...} form that stayed shell text: Woodpecker substitutes it before parsing, and no
// IR reference stands for it.
func (w *wf) noteKept() {
	seen := map[string]bool{}
	for _, f := range w.tx.kept {
		if seen[f] {
			continue
		}
		seen[f] = true
		w.rep.Add(report.Entry{
			IRPath: w.jobPath(), Feature: caps.ScriptInterpolation, Level: caps.Lost, Strategy: "kept as written",
			Detail: f + " is a Woodpecker substitution with no IR reference; it stays in the text as written", SourceLoc: w.file,
		})
	}
}

// noteRuntimeVars reports each Woodpecker run-time variable that the text reads and no IR reference stands for.
func (w *wf) noteRuntimeVars() {
	seen := map[string]bool{}
	for _, n := range w.tx.lostVars {
		if seen[n] {
			continue
		}
		seen[n] = true
		w.rep.Add(report.Entry{
			IRPath: w.jobPath(), Feature: caps.ScriptInterpolation, Level: caps.Lost, Strategy: "kept as written",
			Detail: "Woodpecker run-time variable " + n + " has no IR reference; the target does not set it", SourceLoc: w.file,
		})
	}
}

// concurrency is a workflow concurrency key the IR models: one run at a time in a named group.
type concurrency struct {
	raw   any
	ir    ir.Concurrency
	line  int
	group bool
}

// concurrency reads {limit: 1, group: G}, the form the emitter writes for a pipeline concurrency group.
// Any other shape stays in Ext.
func (w *wf) concurrency(r *reader) *concurrency {
	v, ok := r.take("concurrency")
	if !ok {
		return nil
	}
	m, isMap := asMap(v)
	if isMap && len(m.keys) == 2 {
		limit, _ := text(m.vals["limit"])
		group, hasGroup := text(m.vals["group"])
		if g, err := w.tx.template(group); limit == "1" && hasGroup && err == nil {
			return &concurrency{raw: v, ir: ir.Concurrency{Group: g}, line: r.line("concurrency"), group: true}
		}
	}
	r.release("concurrency")
	return nil
}
