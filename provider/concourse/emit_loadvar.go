package concourse

import (
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// loadVar writes a load_var step back verbatim while the script that stands for it is unchanged.
func (c *chain) loadVar(st *ir.Step, path string) *yaml.Node {
	e := c.jx.pe.e
	if !e.own {
		return nil
	}
	ext := extOf(st.Ext)
	raw, ok := ext[ExtStep]
	if !ok || ext[ExtKind] != "load_var" || !slices.Equal(st.Script, anyStrings(ext[ExtLoadScript])) {
		return nil
	}
	return e.node(path, "the step", raw)
}

// dropLoadRefs removes the export lines that the parser put before the script of a task, so the params that
// the Ext holds are the only form the file gets.
func dropLoadRefs(script []string, ext map[string]any) []string {
	refs := anyStrings(ext[ExtLoadRefs])
	if len(refs) == 0 || !slices.Equal(script[:min(len(refs), len(script))], refs) {
		return script
	}
	return script[len(refs):]
}

func anyStrings(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}
