package tekton_test

import (
	"strings"
	"testing"
)

const goodTemplate = `apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerTemplate
metadata: {name: run-ci}
spec:
  resourcetemplates:
  - apiVersion: tekton.dev/v1
    kind: PipelineRun
    metadata: {generateName: ci-run-}
    spec:
      pipelineRef: {name: ci}
`

func resourcesOf(t *testing.T, files map[string]string) []any {
	t.Helper()
	p, _ := parseFiles(t, files)
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	return res
}

func TestUnmappedTriggerOfMappedListenerIsKept(t *testing.T) {
	el := `apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata: {name: gh, labels: {team: x}}
spec:
  serviceAccountName: sa
  triggers:
  - {name: good, template: {ref: run-ci}}
  - {name: orphan, template: {ref: missing-template}}
`
	files := map[string]string{"p.yaml": triggerFiles, "t.yaml": goodTemplate, "el.yaml": el}
	p, rep := parseFiles(t, files)
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	found := false
	for _, r := range res {
		if strings.Contains(fmtAny(r), "orphan") {
			found = true
		}
	}
	if !found || !hasEntry(rep, "kind: EventListener") {
		t.Fatalf("the unmapped trigger must stay in resources with an entry: %v", res)
	}
	tr := ext(p.Pipelines[0].Triggers[0].Ext)
	if tr["listenerMetadata"] == nil {
		t.Fatalf("EventListener metadata must stay in the trigger Ext: %v", tr)
	}
}

func TestTriggerRefAndDocumentMetadataAreKept(t *testing.T) {
	el := `apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata: {name: gh}
spec:
  serviceAccountName: sa
  triggers:
  - triggerRef: shared
---
apiVersion: triggers.tekton.dev/v1beta1
kind: Trigger
metadata: {name: shared, labels: {a: b}}
spec:
  template: {ref: run-ci}
`
	p, _ := parseFiles(t, map[string]string{"p.yaml": triggerFiles, "t.yaml": goodTemplate, "el.yaml": el})
	tr := ext(p.Pipelines[0].Triggers[0].Ext)
	if tr["triggerRef"] != "shared" || tr["triggerMetadata"] == nil {
		t.Fatalf("trigger ext = %v", tr)
	}
}

func TestDuplicateTaskDocumentIsReportedAndKept(t *testing.T) {
	task := func(img string) string {
		return "apiVersion: tekton.dev/v1\nkind: Task\nmetadata: {name: unit}\nspec:\n  steps: [{name: s, image: " + img + ", script: x}]\n"
	}
	pipe := "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata: {name: ci}\nspec:\n  tasks:\n  - {name: t, taskRef: {name: unit}}\n"
	p, rep := parseFiles(t, map[string]string{"a.yaml": task("first"), "b.yaml": task("second"), "p.yaml": pipe})
	if got := p.Pipelines[0].Jobs[0].Steps[0].Image.Ref; got != "first" {
		t.Fatalf("the first Task must bind, got %q", got)
	}
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	if len(res) != 1 || !hasEntry(rep, "defined twice") {
		t.Fatalf("resources=%v entries=%+v", res, rep.Entries)
	}
}

func TestTaskParamDescriptionIsKept(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec:
      params: [{name: flags, default: -v, description: extra flags}]
      steps: [{name: s, image: x, script: "true"}]
`))
	kept, _ := ext(job(t, pl, "a").Ext)["task_params"].([]any)
	if len(kept) != 1 || !strings.Contains(fmtAny(kept[0]), "extra flags") {
		t.Fatalf("task_params = %v", kept)
	}
}

func TestCronJobMetadataIsKept(t *testing.T) {
	cron := `apiVersion: batch/v1
kind: CronJob
metadata: {name: nightly, namespace: ci, labels: {a: b}}
spec:
  schedule: "0 3 * * *"
  jobTemplate: {spec: {template: {spec: {containers: [{name: p, args: [curl, "el-gh.default.svc:8080"]}]}}}}
`
	el := strings.Replace(listener, "github", "github", 1)
	p, _ := parseFiles(t, map[string]string{"p.yaml": triggerFiles, "t.yaml": el, "c.yaml": cron})
	for _, tr := range p.Pipelines[0].Triggers {
		if c, ok := ext(tr.Ext)["cronjob"].(map[string]any); ok {
			if c["metadata"] == nil {
				t.Fatalf("cronjob = %v", c)
			}
			return
		}
	}
	t.Fatal("no schedule trigger")
}

func TestSidecarImageReadsTaskVariables(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    params: [{name: tag, value: $(params.ctx-tag)}]
    taskSpec:
      params: [{name: tag}]
      sidecars: [{name: db, image: "pg:$(params.tag)"}]
      steps: [{name: s, image: x, script: "true"}]
`))
	if got := job(t, pl, "a").Services[0].Image.Ref; got != "pg:${{ tag }}" {
		t.Fatalf("image = %q", got)
	}
}

func TestTriggersOfOneListenerShareNoState(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"p.yaml": triggerFiles, "t.yaml": goodTemplate, "el.yaml": listener})
	trs := p.Pipelines[0].Triggers
	if len(trs) != 2 {
		t.Fatalf("triggers = %d", len(trs))
	}
	a, _ := ext(trs[0].Ext)["bindings"].([]any)
	b, _ := ext(trs[1].Ext)["bindings"].([]any)
	a[0] = "changed"
	if b[0] == "changed" {
		t.Fatal("bindings are shared between triggers")
	}
}

func TestMatrixWithNonListValueGoesToExt(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(`  params: [{name: versions, type: array}]
  tasks:
  - name: a
    matrix:
      params: [{name: go, value: [[x, y]]}]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
`))
	j := job(t, pl, "a")
	if j.Matrix != nil || ext(j.Ext)["matrix"] == nil || !hasEntry(rep, "matrix") {
		t.Fatalf("matrix=%+v ext=%v", j.Matrix, j.Ext)
	}
}

func TestMatrixParamValueMustBeAList(t *testing.T) {
	for name, val := range map[string]string{
		"array param reference": `"$(params.versions[*])"`,
		"plain scalar":          `"1.22"`,
	} {
		t.Run(name, func(t *testing.T) {
			pl, rep := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    matrix:
      params: [{name: go, value: `+val+`}]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
`))
			j := job(t, pl, "a")
			if j.Matrix != nil || ext(j.Ext)["matrix"] == nil || !hasEntry(rep, "matrix") {
				t.Fatalf("matrix=%+v ext=%v", j.Matrix, j.Ext)
			}
		})
	}
}
