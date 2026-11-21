package tekton_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const triggerFiles = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: ci}
spec:
  tasks:
  - {name: a, taskSpec: {steps: [{name: s, image: x, script: "true"}]}}
`

const listener = `apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata: {name: gh}
spec:
  serviceAccountName: tekton-triggers
  triggers:
  - name: on-push
    interceptors:
    - ref: {name: github, kind: ClusterInterceptor, apiVersion: triggers.tekton.dev}
      params:
      - {name: secretRef, value: {secretName: s, secretKey: k}}
      - {name: eventTypes, value: [push, pull_request]}
    bindings: [{ref: gh-binding}]
    template: {ref: run-ci}
---
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerTemplate
metadata: {name: run-ci}
spec:
  resourcetemplates:
  - apiVersion: tekton.dev/v1
    kind: PipelineRun
    metadata: {generateName: ci-run-}
    spec:
      pipelineRef: {name: ci}
---
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerBinding
metadata: {name: gh-binding}
spec:
  params: [{name: ctx-branch, value: $(extensions.branch)}]
`

func TestEventListenerMapsToTriggers(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"pipeline.yaml": triggerFiles, "triggers.yaml": listener})
	pl := p.Pipelines[0]
	var kinds []ir.TriggerKind
	for _, tr := range pl.Triggers {
		kinds = append(kinds, tr.Kind)
		e := ext(tr.Ext)
		if e["listener"] != "gh" || e["name"] != "on-push" || e["template"] != "run-ci" || e["bindings"] == nil || e["serviceAccountName"] != "tekton-triggers" {
			t.Fatalf("trigger ext = %v", e)
		}
	}
	if !reflect.DeepEqual(kinds, []ir.TriggerKind{ir.TriggerPR, ir.TriggerPush}) {
		t.Fatalf("kinds = %v", kinds)
	}
	if ext(pl.Ext)["resources"] == nil || !hasEntry(rep, "TriggerBinding") {
		t.Fatalf("binding must stay in Ext with an entry: %v", pl.Ext)
	}
}

func TestCELInterceptorIsOpaque(t *testing.T) {
	src := strings.Replace(listener, "    bindings:", "    - cel:\n        filter: \"body.ref == 'refs/heads/main'\"\n    bindings:", 1)
	p, rep := parseFiles(t, map[string]string{"pipeline.yaml": triggerFiles, "triggers.yaml": src})
	for _, tr := range p.Pipelines[0].Triggers {
		f, _ := ext(tr.Ext)["filter"].(string)
		if !strings.HasPrefix(f, "opaque(tekton)") || !strings.Contains(f, "body.ref") {
			t.Fatalf("filter = %q", f)
		}
	}
	if !hasEntry(rep, "CEL filter") {
		t.Fatalf("entries = %+v", rep.Entries)
	}
}

func TestListenerWithoutGitHubIsAPITrigger(t *testing.T) {
	src := strings.Replace(listener, "github", "gitlab", 1)
	p, _ := parseFiles(t, map[string]string{"pipeline.yaml": triggerFiles, "triggers.yaml": src})
	if trs := p.Pipelines[0].Triggers; len(trs) != 1 || trs[0].Kind != ir.TriggerAPI || ext(trs[0].Ext)["interceptors"] == nil {
		t.Fatalf("triggers = %+v", trs)
	}
}

func TestCronJobMapsToSchedule(t *testing.T) {
	cron := `apiVersion: batch/v1
kind: CronJob
metadata: {name: nightly}
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
          - name: post
            image: curlimages/curl
            args: [curl, -X, POST, --data, "{}", "el-gh.default.svc.cluster.local:8080"]
`
	p, _ := parseFiles(t, map[string]string{"pipeline.yaml": triggerFiles, "triggers.yaml": listener, "cron.yaml": cron})
	var sched *ir.Trigger
	for i, tr := range p.Pipelines[0].Triggers {
		if tr.Kind == ir.TriggerSchedule {
			sched = &p.Pipelines[0].Triggers[i]
		}
	}
	if sched == nil || sched.Cron != "0 3 * * *" || ext(sched.Ext)["cronjob"] == nil {
		t.Fatalf("triggers = %+v", p.Pipelines[0].Triggers)
	}
}

func TestPipelinesAsCodeRun(t *testing.T) {
	pl, rep := parseYAML(t, `apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: pac-run
  annotations:
    pipelinesascode.tekton.dev/on-event: "[push]"
    pipelinesascode.tekton.dev/on-target-branch: "[main]"
spec:
  params: [{name: repo_url, value: "{{ repo_url }}"}]
  pipelineSpec:
    tasks:
    - {name: a, taskSpec: {steps: [{name: s, image: x, script: "true"}]}}
`)
	if pl.ID != "pac-run" || len(pl.Jobs) != 1 {
		t.Fatalf("pipeline = %+v", pl)
	}
	e := ext(pl.Ext)
	if e["metadata"] == nil || e["pipelineRun"] == nil || !hasEntry(rep, "Pipelines-as-Code") {
		t.Fatalf("ext=%v entries=%+v", e, rep.Entries)
	}
}

func TestOtherKindsStayInExt(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{
		"pipeline.yaml": triggerFiles,
		"cm.yaml":       "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: cfg}\ndata: {a: b}\n---\napiVersion: v1\nkind: PersistentVolumeClaim\nmetadata: {name: ws}\nspec: {}\n",
	})
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	if len(res) != 2 || !hasEntry(rep, "kind: ConfigMap") {
		t.Fatalf("resources = %v", res)
	}
}
