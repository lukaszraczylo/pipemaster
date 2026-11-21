package tekton_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

func FuzzParse(f *testing.F) {
	f.Add(triggerFiles)
	f.Add(triggerFiles + "---\n" + listener)
	f.Add(pipelineWith(inlineTask + "  finally:\n  - name: n\n    when: [{input: \"$(tasks.status)\", operator: in, values: [Failed]}]\n    taskSpec: {steps: [{name: s, image: x, script: \"echo $(params.ctx-branch)\"}]}\n"))
	f.Add("apiVersion: tekton.dev/v1\nkind: PipelineRun\nmetadata: {generateName: r-}\nspec:\n  pipelineSpec:\n    tasks:\n    - name: a\n      matrix: {params: [{name: g, value: [1, 2]}]}\n      when: [{cel: \"x\"}]\n      taskSpec: {steps: [{name: s, ref: {name: x}}]}\n")
	f.Add("x: &a {name: s}\napiVersion: tekton.dev/v1\nkind: Task\nmetadata: {name: t}\nspec: {steps: [{<<: *a, image: x}]}\n")
	f.Fuzz(func(t *testing.T, src string) {
		p, _, err := tekton.New().Parse(repo(map[string]string{".tekton/p.yaml": src}))
		if err != nil {
			return
		}
		if err := ir.Validate(p); err != nil {
			t.Fatalf("Parse returned an invalid project: %v", err)
		}
		if _, err := ir.Marshal(p); err != nil {
			t.Fatalf("Marshal: %v", err)
		}
	})
}
