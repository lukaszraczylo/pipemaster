package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const argoTestID = "argo-schema-test"

func init() { validate.RegisterArgo(argoTestID, []string{".argo/*.yaml"}) }

const (
	argoDiamond = `apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: dag-diamond-
spec:
  entrypoint: diamond
  templates:
  - name: diamond
    dag:
      tasks:
      - name: A
        template: echo
        arguments:
          parameters: [{name: message, value: A}]
      - name: B
        depends: "A"
        template: echo
        arguments:
          parameters: [{name: message, value: B}]
      - name: C
        depends: "A"
        template: echo
        arguments:
          parameters: [{name: message, value: C}]
      - name: D
        depends: "B && C"
        template: echo
        arguments:
          parameters: [{name: message, value: D}]
  - name: echo
    inputs:
      parameters:
      - name: message
    container:
      image: busybox
      command: [echo, "{{inputs.parameters.message}}"]
`
	argoUndefinedEntrypoint = `apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: nope-
spec:
  entrypoint: nope
  templates:
  - name: main
    container:
      image: busybox
      command: [echo, hi]
`
	argoWorkflowTemplate = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: build
spec:
  entrypoint: main
  templates:
  - name: main
    dag:
      tasks:
      - name: compile
        template: run
      - name: test
        dependencies: [compile]
        template: run
        withSequence:
          count: "3"
  - name: run
    retryStrategy:
      limit: "2"
      backoff:
        duration: 5s
        factor: "2"
    containerSet:
      volumeMounts:
      - name: workspace
        mountPath: /workspace
      containers:
      - name: main
        image: golang:1.27
        command: [sh, -c, go build ./...]
    volumes:
    - name: workspace
      emptyDir: {}
`
	argoCron = `apiVersion: argoproj.io/v1alpha1
kind: CronWorkflow
metadata:
  name: nightly
spec:
  schedules: ["0 3 * * *"]
  workflowSpec:
    workflowTemplateRef:
      name: build
`
	argoSensor = `apiVersion: argoproj.io/v1alpha1
kind: Sensor
metadata:
  name: push
spec:
  dependencies:
  - name: push
    eventSourceName: github
    eventName: push
  triggers:
  - template:
      name: run
      k8s:
        operation: create
        source:
          resource:
            apiVersion: argoproj.io/v1alpha1
            kind: Workflow
            metadata:
              generateName: build-
            spec:
              workflowTemplateRef:
                name: build
`
	argoEventSource = `apiVersion: argoproj.io/v1alpha1
kind: EventSource
metadata:
  name: github
spec:
  github:
    push:
      repositories:
      - owner: acme
        names: [app]
      events: [push]
      webhook:
        endpoint: /push
        port: "12000"
        method: POST
        url: https://hooks.example.com
`
	argoBadTemplate = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: bad
spec:
  entrypoint: main
  templates:
  - name: main
    dag:
      tasks:
      - name: a
        template: run
        dependancies: [b]
        withSequence:
          count: [1]
`
	argoIntForms = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: ints
spec:
  entrypoint: main
  templates:
  - name: main
    dag:
      tasks:
      - name: a
        template: run
        withSequence: {start: 1, end: "3"}
  - name: run
    retryStrategy:
      limit: 2
      backoff: {factor: 2}
    container: {image: busybox}
`
)

var argoValid = map[string]string{
	"workflow.yaml": argoDiamond, "workflowtemplate.yaml": argoWorkflowTemplate, "cron.yaml": argoCron,
	"sensor.yaml": argoSensor, "eventsource.yaml": argoEventSource,
}

func TestArgoValidDocumentsPass(t *testing.T) {
	for name, y := range argoValid {
		t.Run(name, func(t *testing.T) {
			if err := validate.File(argoTestID, name, []byte(y)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArgoClusterWorkflowTemplatePasses(t *testing.T) {
	y := strings.Replace(argoWorkflowTemplate, "kind: WorkflowTemplate", "kind: ClusterWorkflowTemplate", 1)
	if err := validate.File(argoTestID, "cwt.yaml", []byte(y)); err != nil {
		t.Fatal(err)
	}
}

func TestArgoIntOrStringAcceptsBothForms(t *testing.T) {
	if err := validate.File(argoTestID, "ints.yaml", []byte(argoIntForms)); err != nil {
		t.Fatalf("limit, factor and withSequence must take integers and strings: %v", err)
	}
}

func TestArgoInvalidTemplateReportsBothErrors(t *testing.T) {
	err := validate.File(argoTestID, "bad.yaml", []byte(argoBadTemplate))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"dependancies", "count"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestArgoUnknownKindIsSkipped(t *testing.T) {
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  a: b\n"
	if err := validate.File(argoTestID, "cm.yaml", []byte(argoCron+"---\n"+cm)); err != nil {
		t.Fatalf("a known and an unknown kind must pass: %v", err)
	}
	err := validate.File(argoTestID, "cm.yaml", []byte(cm))
	if !errors.Is(err, validate.ErrNoSchema) {
		t.Fatalf("only an unknown kind: got %v, want ErrNoSchema", err)
	}
	if err := validate.File(argoTestID, "bad.yaml", []byte(cm+"---\n"+argoBadTemplate)); err == nil {
		t.Fatal("an unknown kind must not hide an invalid WorkflowTemplate")
	}
}

func TestArgoLint(t *testing.T) {
	for name, y := range map[string]string{
		"workflow.yaml": argoDiamond, "workflowtemplate.yaml": argoWorkflowTemplate,
		// --offline resolves the CronWorkflow reference from the same file.
		"cron.yaml": argoWorkflowTemplate + "---\n" + argoCron,
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := linttool.ArgoLint(t, name, []byte(y)); err != nil {
				t.Fatalf("argo lint rejected a valid file: %v\n%s", err, out)
			}
		})
	}
	out, err := linttool.ArgoLint(t, "nope.yaml", []byte(argoUndefinedEntrypoint))
	if err == nil {
		t.Fatalf("argo lint accepted an undefined entrypoint:\n%s", out)
	}
	if !strings.Contains(out, "template name 'nope' undefined") {
		t.Errorf("argo lint output lacks the undefined template error:\n%s", out)
	}
}

// The schema accepts the undefined entrypoint: only the linter checks references between templates.
func TestArgoSchemaAcceptsWhatOnlyTheLinterRejects(t *testing.T) {
	if err := validate.File(argoTestID, "nope.yaml", []byte(argoUndefinedEntrypoint)); err != nil {
		t.Fatal(err)
	}
}
