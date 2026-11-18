package gha

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// jobStrategy maps strategy to Job.Matrix. A strategy built from ${{ }} expressions (fromJSON
// matrices, say) has no static form, so it stays whole in Ext and the parse report says so.
func (fp *fileParser) jobStrategy(j *ir.Job, m *mapping) error {
	n := m.take("strategy")
	if n == nil {
		return nil
	}
	mx, ok, err := fp.strategy(n)
	if err != nil {
		return err
	}
	if ok {
		j.Matrix = mx
		return nil
	}
	m.release("strategy")
	fp.rep.Add(report.Entry{
		IRPath:    "pipeline/" + fp.stem + "/job/" + j.ID,
		Feature:   caps.JobMatrix,
		Strategy:  "ext",
		Detail:    "strategy needs expression evaluation or has keys the IR does not model; kept in Ext only",
		SourceLoc: fmt.Sprintf("%s:%d", fp.file, m.line("strategy")),
		Level:     caps.Lost,
	})
	return nil
}

func hasExpr(n *yaml.Node) bool {
	n = deref(n)
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode {
		return strings.Contains(n.Value, "${{")
	}
	for _, c := range n.Content {
		if hasExpr(c) {
			return true
		}
	}
	return false
}

// strategy returns ok=false when any part of the strategy needs expression evaluation.
func (fp *fileParser) strategy(n *yaml.Node) (*ir.Matrix, bool, error) {
	if deref(n).Kind != yaml.MappingNode || hasExpr(n) {
		return nil, false, nil
	}
	sm, err := fp.mapping(n, "strategy")
	if err != nil {
		return nil, false, err
	}
	mx := &ir.Matrix{}
	if v := sm.take("fail-fast"); v != nil {
		b, ok := boolNode(v)
		if !ok {
			return nil, false, nil
		}
		mx.FailFast = &b
	}
	if v := sm.take("max-parallel"); v != nil {
		s, _ := scalarText(v)
		p, err := strconv.Atoi(s)
		if err != nil || p < 0 {
			return nil, false, nil
		}
		mx.MaxParallel = p
	}
	mv := sm.take("matrix")
	if len(sm.entries) > len(sm.used) {
		return nil, false, nil
	}
	if mv != nil {
		ok, err := fp.matrix(mx, mv)
		if err != nil || !ok {
			return nil, false, err
		}
	}
	return mx, true, nil
}

func (fp *fileParser) matrix(mx *ir.Matrix, n *yaml.Node) (bool, error) {
	if deref(n).Kind != yaml.MappingNode {
		return false, nil
	}
	mm, err := fp.mapping(n, "matrix")
	if err != nil {
		return false, err
	}
	for _, e := range mm.entries {
		switch e.key {
		case "include", "exclude":
			combos, ok, err := fp.combos(e.val)
			if err != nil || !ok {
				return false, err
			}
			if e.key == "include" {
				mx.Include = combos
			} else {
				mx.Exclude = combos
			}
		default:
			if e.val.Kind != yaml.SequenceNode {
				return false, nil
			}
			vals, err := fp.axisValues(e.val)
			if err != nil {
				return false, err
			}
			mx.Axes = append(mx.Axes, ir.Axis{Name: e.key, Values: vals})
		}
	}
	return true, nil
}

func (fp *fileParser) axisValues(n *yaml.Node) ([]string, error) {
	out := make([]string, 0, len(n.Content))
	for _, c := range n.Content {
		s, err := fp.matrixValue(c)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// matrixValue renders a scalar as text and any other node as compact JSON with sorted object keys.
func (fp *fileParser) matrixValue(n *yaml.Node) (string, error) {
	if s, ok := scalarText(n); ok {
		return s, nil
	}
	v, err := fp.plain(n)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fp.errf(n, "matrix value: %v", err)
	}
	return string(b), nil
}

func (fp *fileParser) combos(n *yaml.Node) ([]ir.Combo, bool, error) {
	if deref(n).Kind != yaml.SequenceNode {
		return nil, false, nil
	}
	var out []ir.Combo
	for _, c := range n.Content {
		cm, err := fp.mapping(c, "matrix include/exclude entry")
		if err != nil {
			return nil, false, err
		}
		combo := make(ir.Combo, 0, len(cm.entries))
		for _, e := range cm.entries {
			v, err := fp.matrixValue(e.val)
			if err != nil {
				return nil, false, err
			}
			combo = append(combo, ir.KV{Key: e.key, Value: v})
		}
		sort.SliceStable(combo, func(a, b int) bool { return combo[a].Key < combo[b].Key })
		out = append(out, combo)
	}
	return out, true, nil
}
