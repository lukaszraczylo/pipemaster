package gitlab

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
	"go.yaml.in/yaml/v3"
)

const (
	extSpec     = "spec"
	specInputs  = "inputs"
	inputType   = "type"
	inputDef    = "default"
	inputDesc   = "description"
	typeBoolean = "boolean"
	typeNumber  = "number"
)

// specInputs reads the spec:inputs of a header document. The raw header stays in Ext so keys the IR has no
// field for (options, regex, rules) come back with it.
func specInputList(spec map[string]any) []ir.Input {
	raw, _ := spec[specInputs].(map[string]any)
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	slices.Sort(names)
	var out []ir.Input
	for _, n := range names {
		out = append(out, inputFromSpec(n, raw[n]))
	}
	return out
}

func inputFromSpec(name string, v any) ir.Input {
	in := ir.Input{Name: name}
	m, _ := v.(map[string]any)
	if t, ok := m[inputType].(string); ok {
		in.Type = t
	}
	if d, ok := m[inputDef]; ok {
		switch d.(type) {
		case map[string]any, []any:
			// The structure stays in the kept header; flattened text would reach other providers as a value.
		default:
			in.Default = fmt.Sprint(d)
		}
	} else {
		in.Required = true
	}
	if d, ok := m[inputDesc].(string); ok {
		in.Description = d
	}
	return in
}

// specNode writes the header document: the raw header with the inputs the IR holds now. An input equal to
// what the raw one parses to keeps its raw form.
func specNode(spec map[string]any, inputs []ir.Input) (*yaml.Node, error) {
	raw, _ := spec[specInputs].(map[string]any)
	out := map[string]any{}
	for k, v := range spec {
		if k != specInputs {
			out[k] = v
		}
	}
	ins := yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, in := range inputs {
		var node *yaml.Node
		var err error
		if old, ok := raw[in.Name]; ok && inputFromSpec(in.Name, old) == in {
			node, err = plainNode(old)
		} else {
			node, err = plainNode(inputEntry(in, raw[in.Name]))
		}
		if err != nil {
			return nil, err
		}
		ins.Content = append(ins.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: in.Name}, node)
	}
	body, err := plainNode(out)
	if err != nil {
		return nil, err
	}
	if len(ins.Content) > 0 {
		body.Content = append([]*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: specInputs}, &ins}, body.Content...)
	}
	root := yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: extSpec}, body}
	return &root, nil
}

// inputEntry builds the mapping of an input the IR changed, keeping the raw keys it has no field for.
func inputEntry(in ir.Input, old any) map[string]any {
	m := map[string]any{}
	if om, ok := old.(map[string]any); ok {
		for k, v := range om {
			if k != inputType && k != inputDef && k != inputDesc {
				m[k] = v
			}
		}
	}
	if in.Type != "" {
		m[inputType] = in.Type
	}
	if !in.Required {
		m[inputDef] = typedDefault(in)
	}
	if in.Description != "" {
		m[inputDesc] = in.Description
	}
	return m
}

func typedDefault(in ir.Input) any {
	switch in.Type {
	case typeBoolean:
		if b, err := strconv.ParseBool(in.Default); err == nil {
			return b
		}
	case typeNumber:
		if f, err := strconv.ParseFloat(in.Default, 64); err == nil {
			return f
		}
	}
	return in.Default
}

// reportStructuredDefaults reports each input whose default is a list or mapping: Input.Default holds text only,
// so the structure lives in the kept header and does not reach a provider that reads the IR.
func (s *state) reportStructuredDefaults(spec map[string]any) {
	raw, _ := spec[specInputs].(map[string]any)
	for _, n := range slices.Sorted(maps.Keys(raw)) {
		m, _ := raw[n].(map[string]any)
		switch m[inputDef].(type) {
		case map[string]any, []any:
			s.rep.Add(report.Entry{
				IRPath: "pipeline/" + pipelineID + "/input/" + n, Feature: caps.ExtPassthrough, Level: caps.Lost,
				Strategy: "kept in Ext[gitlab][spec]", Detail: "spec input " + n + " has a list or mapping default; the IR default is flattened text",
			})
		}
	}
}
