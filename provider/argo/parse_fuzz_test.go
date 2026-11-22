package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
)

func FuzzParse(f *testing.F) {
	f.Add(dagOf("      - {name: a, template: echo}\n      - {name: b, template: echo, depends: \"a.Failed || !a.Skipped\", when: \"{{workflow.parameters.x}} == y\"}\n", echoTemplates))
	f.Add(ciTemplate + "---\n" + cron("  schedules: ['* * * * *']\n  workflowSpec: {workflowTemplateRef: {name: ci}}\n"))
	f.Add(ciTemplate + "---\n" + eventSource + "---\n" + sensor(dep("      - {path: body.ref, type: string, value: [\"refs/heads/main\"]}\n"), runCI))
	f.Add(wt("  - name: main\n    steps:\n    - - {name: a, template: t}\n    - - {name: b, template: t}\n  - name: t\n    containerSet: {containers: [{name: x, image: i, dependencies: [y]}, {name: y, image: i}]}\n"))
	f.Add(wt("  - {name: main, dag: {tasks: [{name: g, template: gate}, {name: d, template: main, dependencies: [g]}]}}\n  - {name: gate, suspend: {}}\n"))
	f.Add("x: &a {name: s}\napiVersion: argoproj.io/v1alpha1\nkind: Workflow\nmetadata: {generateName: r-}\nspec: {entrypoint: m, templates: [{<<: *a, container: {image: x}}]}\n")
	f.Fuzz(func(t *testing.T, src string) {
		p, _, err := argo.New().Parse(repo(map[string]string{".argo/p.yaml": src}))
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
