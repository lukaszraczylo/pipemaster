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
	job  *ir.Job
	when *whenSpec
}

// wf is the state of one workflow parse.
type wf struct {
	tx   *texts
	rep  *report.Report
	ext  *sink
	file string
	id   string
}

func (w *wf) jobPath() string { return pipelinePth + "/job/" + w.id }

func parseWorkflow(rep *report.Report, file string, data []byte) (*workflow, error) {
	doc, err := parseDocument(data)
	if err != nil {
		return nil, err
	}
	top, ok := asMap(doc)
	if !ok {
		return nil, fmt.Errorf("a workflow must be a mapping")
	}
	r := newReader(top)
	w := &wf{tx: &texts{axes: map[string]bool{}}, rep: rep, file: file, id: workflowName(file)}
	w.ext = newSink(rep, file, w.jobPath())
	j := &ir.Job{ID: w.id}
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
	if v, ok := r.take("services"); ok {
		if err := w.services(j, v); err != nil {
			return nil, err
		}
	}
	w.labels(j, r)
	if err := w.needs(j, r); err != nil {
		return nil, err
	}
	spec, err := w.when(r)
	if err != nil {
		return nil, err
	}
	w.ext.keepRest(r, map[string]caps.Feature{"concurrency": caps.PipelineConcurrency})
	w.noteKept()
	j.Ext = w.ext.ext()
	return &workflow{job: j, when: spec}, nil
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
			mx.Axes = append(mx.Axes, ir.Axis{Name: k, Values: vals})
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
			c = append(c, ir.KV{Key: k, Value: t})
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
		opt, _ := boolOf(m.vals["optional"])
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
