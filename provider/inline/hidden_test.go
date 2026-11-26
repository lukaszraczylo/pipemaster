package inline_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
)

func TestFlattenUnsetsHiddenParentVariablesInChildScripts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger map[string]any
		script  string
		want    string
	}{
		{"hidden", map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false}}, "echo s2", "unset -v PARENT\necho s2"},
		{"hidden after shebang", map[string]any{"include": "ci/child.yml", "forward": map[string]any{"yaml_variables": false}}, "#!/bin/sh\necho s2", "#!/bin/sh\nunset -v PARENT\necho s2"},
		{"forwarded", map[string]any{"include": "ci/child.yml", "forward": map[string]any{"pipeline_variables": true}}, "echo s2", "echo s2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := gitlabProject(tc.trigger)
			src.Pipelines[0].Env = []ir.Var{{Name: "PARENT", Value: ir.Value{Lit: "x"}}}
			find(t, src, "call").Child.Pipeline.Jobs[1].Steps[0].Script = []string{tc.script}
			flat, _ := inline.Flatten(src, "drone")
			got := strings.Join(find(t, flat, "call-s2").Steps[0].Script, "\n")
			if got != tc.want {
				t.Errorf("script = %q, want %q", got, tc.want)
			}
		})
	}
}
