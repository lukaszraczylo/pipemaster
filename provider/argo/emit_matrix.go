package argo

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// matrixArg is the matrix that the emitter wrote as withItems. The parser puts it back when the file still
// holds exactly the cells that the matrix expands to.
type matrixArg struct {
	FailFast    *bool      `json:"fail_fast,omitempty"`
	Axes        []ir.Axis  `json:"axes,omitempty"`
	Include     []ir.Combo `json:"include,omitempty"`
	Exclude     []ir.Combo `json:"exclude,omitempty"`
	MaxParallel int        `json:"max_parallel,omitempty"`
}

func argOf(m *ir.Matrix) matrixArg {
	return matrixArg{FailFast: m.FailFast, Axes: m.Axes, Include: m.Include, Exclude: m.Exclude, MaxParallel: m.MaxParallel}
}

func (a matrixArg) matrix() *ir.Matrix {
	return &ir.Matrix{FailFast: a.FailFast, Axes: a.Axes, Include: a.Include, Exclude: a.Exclude, MaxParallel: a.MaxParallel}
}

// cellKeys lists the keys of the cells in order of first use.
func cellKeys(cells []ir.Combo) []string {
	var keys []string
	seen := map[string]bool{}
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

// unsafeValue is true for a value that Argo would re-read as a tag after substitution.
func unsafeValue(s string) bool { return strings.Contains(s, tagOpen) || strings.Contains(s, tagClose) }

// matrixItems writes the expanded cells as withItems maps. A cell with a value that holds a tag is dropped and
// reported, because the value would be read as a tag when it reaches the template. ok is false when an axis name
// is no valid parameter name, which disables the task.
func (tc *jobCtx) matrixItems() (items *yaml.Node, ok, whole bool) {
	m, path := tc.jp.j.Matrix, tc.jp.path
	cells := ir.Expand(m)
	keys := cellKeys(cells)
	for _, k := range keys {
		if !tc.declareMatrix(k) {
			tc.e.lose(path, caps.JobMatrix, "the axis name "+k+" is not a valid parameter name; the task is disabled with when {{= false }}")
			return nil, false, false
		}
	}
	whole = true
	items = seqOf()
	dropped := 0
	for _, c := range cells {
		vals := map[string]string{}
		bad := false
		for _, kv := range c {
			vals[kv.Key] = kv.Value
			bad = bad || unsafeValue(kv.Value)
		}
		if bad {
			dropped++
			whole = false
			continue
		}
		cell := newMap()
		for _, k := range keys {
			cell.setStr(k, vals[k])
		}
		items.Content = append(items.Content, cell.n)
	}
	if dropped > 0 {
		tc.e.lose(path, caps.JobMatrix, "a matrix value holds {{ or }}, which Argo reads as a tag after substitution; the task drops "+itoa(dropped)+" cell(s) that hold one")
	}
	detail := "the cells run as withItems of one task"
	if m.FailFast != nil || m.MaxParallel > 0 {
		detail += "; fail-fast and max-parallel have no form on one task, so every cell runs and a failing cell fails the task"
	}
	tc.e.emulate(path, caps.JobMatrix, "withItems over the expanded cells", detail)
	return items, true, whole
}
