package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const richWorkflow = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci, labels: {team: a}}
spec:
  entrypoint: main
  serviceAccountName: runner
  arguments:
    parameters:
    - {name: ver, value: "1", description: version}
    - {name: ctx-branch}
  templates:
  - name: main
    dag:
      tasks:
      - name: build
        template: build
      - name: test
        template: test
        dependencies: [build]
        arguments:
          parameters: [{name: v, value: "{{tasks.build.outputs.parameters.digest}}"}]
        when: "{{workflow.parameters.ctx-branch}} == main"
  - name: build
    retryStrategy: {limit: "2", retryPolicy: Always}
    timeout: 5m
    outputs:
      parameters: [{name: digest, valueFrom: {path: /tmp/d}}]
    container:
      image: golang:1.27
      command: [sh, -c]
      args: ["go build && echo hi > /tmp/d"]
      resources: {limits: {cpu: "1"}}
  - name: test
    inputs:
      parameters: [{name: v}]
    containerSet:
      containers:
      - {name: one, image: alpine, command: [echo, one]}
      - {name: two, image: alpine, command: [echo, two], dependencies: [one]}
`

func originFiles() map[string]string {
	return map[string]string{
		".argo/t.yaml":  richWorkflow,
		".argo/c.yaml":  cron("  schedules: [\"0 2 * * *\"]\n  timezone: Europe/London\n  workflowSpec: {workflowTemplateRef: {name: ci}}\n"),
		".argo/es.yaml": eventSource,
		".argo/s.yaml":  sensor(dep("      - {path: body.ref, type: string, value: [\"^refs/heads/main$\"]}\n"), runCI),
		".argo/x.yaml":  "apiVersion: v1\nkind: ConfigMap\nmetadata: {name: extra}\ndata: {a: b}\n",
	}
}

func TestArgoFilesSurviveAnArgoRoundTrip(t *testing.T) {
	first, _, err := parseRepo(originFiles())
	if err != nil {
		t.Fatal(err)
	}
	files, _ := emit(t, first)
	second, _, err := parseRepo(files)
	if err != nil {
		t.Fatalf("%v\n%s", err, joinFiles(files))
	}
	if ok, diff := ir.Equal(first, second); !ok {
		t.Fatalf("the Argo round trip changed the IR:\n%s\n%s", diff, joinFiles(files))
	}
	all := joinFiles(files)
	for _, want := range []string{"resources:", "retryPolicy: Always", "Europe/London", "kind: ConfigMap", "gh-dep", "operate-workflow-sa", "apiToken"} {
		if !strings.Contains(all, want) {
			t.Errorf("the emitted files lack %q", want)
		}
	}
}

func TestEmitOfAnArgoRoundTripIsStable(t *testing.T) {
	first, _, err := parseRepo(originFiles())
	if err != nil {
		t.Fatal(err)
	}
	one, _ := emit(t, first)
	second, _, err := parseRepo(one)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := emit(t, second)
	if joinFiles(one) != joinFiles(two) {
		t.Errorf("a second trip changed the files:\n%s\n=====\n%s", joinFiles(one), joinFiles(two))
	}
}
