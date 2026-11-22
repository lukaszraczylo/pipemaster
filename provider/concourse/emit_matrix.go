package concourse

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	cellVar      = "matrix"
	acrossAll    = "all"
	maxInFlightK = "max_in_flight"
)

// matrixSpec is the across step that stands for the job matrix.
type matrixSpec struct {
	across   *yaml.Node
	names    map[string]bool
	yaml     string
	cells    bool
	failFast bool
}

// setupMatrix builds the across list for the job matrix. A matrix of plain axes is one var per axis; one with
// include or exclude lists is one var that holds the cells. A name that is no var name loses the matrix.
func (jx *jobEmitter) setupMatrix() {
	m := jx.j.Matrix
	if m == nil {
		return
	}
	e := jx.pe.e
	body, err := carryYAML(m)
	if err != nil {
		e.lose(jx.path, caps.JobMatrix, "the matrix marker cannot be written: "+err.Error())
		return
	}
	spec := &matrixSpec{names: map[string]bool{}, yaml: body, cells: len(m.Include)+len(m.Exclude) > 0}
	if m.FailFast != nil && *m.FailFast {
		spec.failFast = true
	}
	var items []*yaml.Node
	if spec.cells {
		cells := ir.Expand(m)
		keys := cellKeys(cells)
		var vals []*yaml.Node
		for _, c := range cells {
			cell := newMap()
			byKey := map[string]string{}
			for _, kv := range c {
				byKey[kv.Key] = kv.Value
			}
			for _, k := range keys {
				cell.SetStr(k, byKey[k])
			}
			vals = append(vals, cell.N)
		}
		for _, k := range keys {
			if !nameRE.MatchString(k) {
				e.lose(jx.path, caps.JobMatrix, "the matrix key "+k+" is not a Concourse variable name; the job runs once with no matrix")
				return
			}
			spec.names[k] = true
		}
		if len(vals) == 0 {
			e.lose(jx.path, caps.JobMatrix, "the matrix has no cell; the job runs once with no matrix")
			return
		}
		items = append(items, acrossItem(cellVar, vals, m.MaxParallel, true))
	} else {
		for i, a := range m.Axes {
			if !nameRE.MatchString(a.Name) || len(a.Values) == 0 {
				e.lose(jx.path, caps.JobMatrix, "the matrix axis "+a.Name+" is not a Concourse variable name or has no value; the job runs once with no matrix")
				return
			}
			spec.names[a.Name] = true
			items = append(items, acrossItem(a.Name, strNodes(a.Values), m.MaxParallel, i == 0))
		}
		if len(items) == 0 {
			e.lose(jx.path, caps.JobMatrix, "the matrix has no axis; the job runs once with no matrix")
			return
		}
	}
	if m.MaxParallel > 0 && len(items) > 1 {
		e.emulate(jx.path, caps.JobMatrix, "across over the axes", "max_parallel is the max_in_flight of the first axis, so the cells in flight can exceed it")
	}
	spec.across = seqOf(items...)
	spec.cells = spec.cells && true
	jx.mx = spec
}

func strNodes(l []string) []*yaml.Node {
	out := make([]*yaml.Node, len(l))
	for i, s := range l {
		out[i] = str(s)
	}
	return out
}

func cellKeys(cells []ir.Combo) []string {
	seen := map[string]bool{}
	var keys []string
	for _, c := range cells {
		for _, kv := range c {
			if !seen[kv.Key] {
				seen[kv.Key] = true
				keys = append(keys, kv.Key)
			}
		}
	}
	return keys
}

func acrossItem(name string, values []*yaml.Node, maxParallel int, first bool) *yaml.Node {
	m := newMap()
	m.SetStr("var", name)
	m.Set("values", seqOf(values...))
	if first && maxParallel > 0 {
		m.Set(maxInFlightK, intNode(maxParallel))
	} else {
		m.SetStr(maxInFlightK, acrossAll)
	}
	return m.N
}

// matrixValue is the local var that a task param reads for a matrix key.
func (jx *jobEmitter) matrixValue(key string) (string, bool) {
	if jx.mx == nil || !jx.mx.names[key] {
		return "", false
	}
	if jx.mx.cells {
		return "((.:" + cellVar + "." + key + "))", true
	}
	return "((.:" + key + "))", true
}

// wrap puts the tasks in a do step with the across modifier.
func (m *matrixSpec) wrap(body []*yaml.Node) *yaml.Node {
	out := newMap()
	out.Set("do", seqOf(body...))
	out.Set("across", m.across)
	if m.failFast {
		out.Set("fail_fast", boolNode(true))
	}
	return out.N
}
