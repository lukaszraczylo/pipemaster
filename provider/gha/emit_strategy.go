package gha

import (
	"encoding/json"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func (e *emitter) strategyKey(m *omap, _ string, j *ir.Job, _ map[string]any, consumed map[string]bool) error {
	mx := j.Matrix
	if mx == nil {
		return nil
	}
	sm := newMap()
	mm := newMap()
	for _, ax := range mx.Axes {
		seq := seqOf()
		for _, v := range ax.Values {
			n, err := matrixValue(v)
			if err != nil {
				return err
			}
			seq.Content = append(seq.Content, n)
		}
		mm.set(ax.Name, seq)
	}
	for key, combos := range map[string][]ir.Combo{"include": mx.Include, "exclude": mx.Exclude} {
		if len(combos) == 0 {
			continue
		}
		seq := seqOf()
		for _, c := range combos {
			cm := newMap()
			for _, kv := range c {
				n, err := matrixValue(kv.Value)
				if err != nil {
					return err
				}
				cm.set(kv.Key, n)
			}
			seq.Content = append(seq.Content, cm.n)
		}
		mm.set(key, seq)
	}
	if !mm.empty() {
		sm.set("matrix", orderedMatrix(mm))
	}
	if mx.FailFast != nil {
		sm.set("fail-fast", boolNodeOf(*mx.FailFast))
	}
	if mx.MaxParallel > 0 {
		sm.set("max-parallel", intNode(mx.MaxParallel))
	}
	m.set("strategy", sm.n)
	consumed["strategy"] = true
	return nil
}

// orderedMatrix puts include and exclude after the axes, include first, whatever order the map iteration gave.
func orderedMatrix(m *omap) *yaml.Node {
	out := newMap()
	var inc, exc *yaml.Node
	for i := 0; i+1 < len(m.n.Content); i += 2 {
		k, v := m.n.Content[i], m.n.Content[i+1]
		switch k.Value {
		case "include":
			inc = v
		case "exclude":
			exc = v
		default:
			out.set(k.Value, v)
		}
	}
	if inc != nil {
		out.set("include", inc)
	}
	if exc != nil {
		out.set("exclude", exc)
	}
	return out.n
}

// matrixValue writes a JSON object or array value back as YAML structure when the text is exactly
// what the parser's compact JSON rendering would produce.
func matrixValue(s string) (*yaml.Node, error) {
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			if b, err := json.Marshal(v); err == nil && string(b) == s {
				return plainNode(v)
			}
		}
	}
	return str(s), nil
}
