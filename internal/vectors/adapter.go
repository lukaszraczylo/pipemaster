package vectors

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// MinAdapterCases is the fewest cases an adapter vector file may hold; a smaller file means the enumeration broke.
const MinAdapterCases = 150

// Var is a literal variable as the abstract ASTs list it.
type Var struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Entry is one key and value of a YAML mapping, in file order.
type Entry struct {
	Val *yaml.Node
	Key string
}

// Entries lists the entries of a mapping node.
func Entries(n *yaml.Node) ([]Entry, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: want a mapping", n.Line)
	}
	out := make([]Entry, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, Entry{Key: n.Content[i].Value, Val: n.Content[i+1]})
	}
	return out, nil
}

// Strings reads a scalar as one string or a sequence of scalars.
func Strings(n *yaml.Node) ([]string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}, nil
	case yaml.SequenceNode:
		out := []string{}
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: want a scalar", c.Line)
			}
			out = append(out, c.Value)
		}
		return out, nil
	}
	return nil, fmt.Errorf("line %d: want a scalar or a sequence", n.Line)
}

// Vars reads a mapping of names to literal values in file order.
func Vars(n *yaml.Node) ([]Var, error) {
	es, err := Entries(n)
	if err != nil {
		return nil, err
	}
	out := []Var{}
	for _, e := range es {
		if e.Val.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("line %d: variable %s is not a scalar", e.Val.Line, e.Key)
		}
		out = append(out, Var{Name: e.Key, Value: e.Val.Value})
	}
	return out, nil
}

// Int reads a scalar integer.
func Int(n *yaml.Node) (int, error) {
	v, err := strconv.Atoi(n.Value)
	if err != nil || n.Kind != yaml.ScalarNode {
		return 0, fmt.Errorf("line %d: want an integer, have %q", n.Line, n.Value)
	}
	return v, nil
}

// Bool reads a scalar boolean.
func Bool(n *yaml.Node) (bool, error) {
	v, err := strconv.ParseBool(n.Value)
	if err != nil || n.Kind != yaml.ScalarNode {
		return false, fmt.Errorf("line %d: want a boolean, have %q", n.Line, n.Value)
	}
	return v, nil
}

// RootMapping parses src as one YAML document whose root is a mapping.
func RootMapping(src []byte) ([]Entry, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, fmt.Errorf("want one document")
	}
	return Entries(doc.Content[0])
}

// Mark is one emulation marker with the argument lines that follow it.
type Mark struct {
	Feature string
	Path    string
	Args    []string
}

// Marks lists the distinct markers of src in order of first appearance. Repeats of a marker add no
// information, and the model lists each marker once.
func Marks(src []byte) []Mark {
	args := emitkit.ParseArgs(src)
	var out []Mark
	seen := map[ir.FeatureUse]bool{}
	for _, u := range emitkit.ParseMarkers(src) {
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, Mark{Feature: string(u.Feature), Path: u.IRPath, Args: args[u]})
	}
	return out
}

// DecodeArg reads the JSON argument of a marker.
func (m Mark) DecodeArg(v any) error {
	if len(m.Args) != 1 {
		return fmt.Errorf("marker %s %s has %d arguments, want 1", m.Feature, m.Path, len(m.Args))
	}
	return json.Unmarshal([]byte(m.Args[0]), v)
}

// DiffCore fails the test when the core pipeline of a project differs from the vector's.
func DiffCore(t *testing.T, label string, want json.RawMessage, got *ir.CorePipeline) {
	t.Helper()
	var w ir.CorePipeline
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: decode vector core: %v", label, err)
	}
	if d := cmp.Diff(&w, got, cmpopts.EquateEmpty()); d != "" {
		t.Errorf("%s: core IR differs from the Lean model (-lean +go):\n%s", label, d)
	}
}

// DiffAST fails the test when the decoded adapter output differs from the vector's abstract AST.
func DiffAST[T any](t *testing.T, label string, want json.RawMessage, got T) {
	t.Helper()
	var w T
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: decode vector AST: %v", label, err)
	}
	if d := cmp.Diff(w, got, cmpopts.EquateEmpty()); d != "" {
		t.Errorf("%s: emitted output differs from the Lean model (-lean +go):\n%s", label, d)
	}
}
