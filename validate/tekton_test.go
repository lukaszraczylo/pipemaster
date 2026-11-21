package validate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const tektonTestID = "tekton-schema-test"

func init() { validate.RegisterTekton(tektonTestID, []string{".tekton/*.yaml"}) }

const (
	validPipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: build
spec:
  params:
    - name: ctx-branch
      type: string
      default: main
  tasks:
    - name: compile
      taskSpec:
        steps:
          - name: run
            image: golang:1.27
            script: go build ./...
    - name: test
      runAfter: [compile]
      retries: 2
      taskRef:
        name: go-test
`
	validTask = `apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: go-test
spec:
  steps:
    - name: run
      image: golang:1.27
      script: go test ./...
`
	validEventListener = `apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata:
  name: listener
spec:
  serviceAccountName: tekton-triggers
  triggers:
    - name: push
      bindings:
        - ref: push-binding
      template:
        ref: push-template
`
	validCronJob = `apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: trigger
              image: curlimages/curl:8.11.0
              args: ["-X", "POST", "http://el-listener.default.svc:8080"]
`
	badPipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: bad
spec:
  tasks:
    - name: a
      taskRef:
        name: t
    - name: b
      taskRef:
        name: t
      runafter: [a]
      retries: "two"
`
)

func TestTektonValidDocumentsPass(t *testing.T) {
	for name, y := range map[string]string{
		"pipeline.yaml": validPipeline, "task.yaml": validTask,
		"eventlistener.yaml": validEventListener, "cronjob.yaml": validCronJob,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate.File(tektonTestID, name, []byte(y)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTektonInvalidPipelineReportsBothErrors(t *testing.T) {
	err := validate.File(tektonTestID, "bad.yaml", []byte(badPipeline))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"runafter", "retries"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestTektonUnknownKindIsSkipped(t *testing.T) {
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  a: b\n"
	if err := validate.File(tektonTestID, "cm.yaml", []byte(validTask+"---\n"+cm)); err != nil {
		t.Fatalf("a known and an unknown kind must pass: %v", err)
	}
	err := validate.File(tektonTestID, "cm.yaml", []byte(cm))
	if !errors.Is(err, validate.ErrNoSchema) {
		t.Fatalf("only an unknown kind: got %v, want ErrNoSchema", err)
	}
	if err := validate.File(tektonTestID, "bad.yaml", []byte(cm+"---\n"+badPipeline)); err == nil {
		t.Fatal("an unknown kind must not hide an invalid Pipeline")
	}
}

func TestTektonKubeconform(t *testing.T) {
	for name, y := range map[string]string{
		"pipeline.yaml": validPipeline, "task.yaml": validTask,
		"eventlistener.yaml": validEventListener, "cronjob.yaml": validCronJob,
	} {
		t.Run(name, func(t *testing.T) {
			if out, err := linttool.KubeconformLint(t, name, []byte(y)); err != nil {
				t.Fatalf("kubeconform rejected a valid file: %v\n%s", err, out)
			}
		})
	}
	out, err := linttool.KubeconformLint(t, "bad.yaml", []byte(badPipeline))
	if err == nil {
		t.Fatalf("kubeconform accepted an invalid Pipeline:\n%s", out)
	}
	for _, want := range []string{"runafter", "got string, want integer"} {
		if !strings.Contains(out, want) {
			t.Errorf("kubeconform output lacks %q:\n%s", want, out)
		}
	}
}
