package tekton_test

import (
	"reflect"
	"testing"
)

func TestUnusedBoundParamStaysInExt(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    params: [{name: unused, value: v1}, {name: arr, value: [x, y]}]
    taskSpec:
      params: [{name: unused}, {name: arr, type: array}]
      steps: [{name: s, image: x, script: "true"}]
`))
	e := ext(job(t, pl, "a").Ext)
	if !reflect.DeepEqual(e["unused_params"], map[string]any{"unused": "v1"}) || e["array_params"] == nil || e["task_params"] == nil {
		t.Fatalf("ext = %v", e)
	}
	if !hasEntry(rep, "do not read are kept") && !hasEntry(rep, "does not read") {
		t.Fatalf("entries = %+v", rep.Entries)
	}
}

func TestTektonVariablesWithoutIRFormStayText(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec:
      params: [{name: unbound}]
      steps:
      - name: s
        image: x
        script: echo $(params.unbound) $(workspaces.src.path) $(date) $(echo $(params.ctx-tag))
`))
	got := step(t, job(t, pl, "a"), "s").Script
	want := []string{"echo $(params.unbound) $(workspaces.src.path) $(date) $(echo ${{ tag }})"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("script = %q", got)
	}
	if !hasEntry(rep, "$(workspaces.src.path)") || hasEntry(rep, "$(date)") {
		t.Fatalf("only Tekton variables get an entry: %+v", rep.Entries)
	}
}

func TestUnnamedStepGetsDerivedID(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith("  tasks:\n  - name: a\n    taskSpec: {steps: [{image: x, script: one}, {image: x, script: two}]}\n"))
	j := job(t, pl, "a")
	if len(j.Steps) != 2 || j.Steps[0].ID != "a-step-1" || j.Steps[1].ID != "a-step-2" {
		t.Fatalf("steps = %+v", j.Steps)
	}
}

func TestStepWhenAndDuplicateTask(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec:
      steps:
      - name: s
        image: x
        script: "true"
        when: [{input: "$(params.ctx-event)", operator: in, values: [push]}]
`))
	if got := cond(step(t, job(t, pl, "a"), "s").If); got != "event == 'push'" {
		t.Fatalf("step if = %q", got)
	}
	parseErr(t, pipelineWith("  tasks:\n  - {name: a, taskSpec: {steps: [{name: s, image: x, script: x}]}}\n  - {name: a, taskSpec: {steps: [{name: s, image: x, script: x}]}}\n"))
}

func TestPipelineTaskWithoutBody(t *testing.T) {
	parseErr(t, pipelineWith("  tasks:\n  - name: a\n"))
}

const edgePipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: p
spec:
  tasks:
    - name: a
      taskSpec:
        steps: [{name: s, image: x, script: "true"}]
`

func TestTrailingEmptyDocumentIsSkipped(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"p.yaml": edgePipeline + "---\n"})
	if len(p.Pipelines) != 1 {
		t.Fatalf("pipelines = %d", len(p.Pipelines))
	}
}

func TestUnquotedTemplateVariableReadsAsText(t *testing.T) {
	src := `apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: r
spec:
  params:
    - name: repo_url
      value: {{repo_url}}
  pipelineSpec:
    tasks:
      - name: a
        taskSpec:
          steps: [{name: s, image: x, script: "true"}]
`
	p, _ := parseFiles(t, map[string]string{"r.yaml": src})
	if len(p.Pipelines) != 1 {
		t.Fatalf("pipelines = %d", len(p.Pipelines))
	}
}
