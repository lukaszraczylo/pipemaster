package argo_test

import (
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const valueFromTemplate = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata:
  name: ci
spec:
  entrypoint: main
  templates:
    - name: main
      dag:
        tasks:
          - name: build
            template: build
    - name: build
      containerSet:
        containers:
          - name: main
            image: alpine:3.22
            command: [sh, -ec]
            args: ["echo hi"]
            env:
              - name: TOK
                valueFrom:
                  secretKeyRef:
                    name: creds
                    key: token
              - name: X
                value: "1"
`

func TestArgoReEmitKeepsEnvValueFrom(t *testing.T) {
	p, _, err := parseRepo(map[string]string{".argo/workflows/ci.yaml": valueFromTemplate})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := emit(t, p)
	out := files[".argo/workflows/ci.yaml"]
	for _, want := range []string{"name: TOK", "secretKeyRef", "key: token", "name: X"} {
		if !strings.Contains(out, want) {
			t.Errorf("re-emit lacks %q:\n%s", want, out)
		}
	}
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("second trip changed the IR:\n%s", diff)
	}
}

const refName = "DEPLOY_REF"

func TestSecretEnvSurvivesTwoArgoTrips(t *testing.T) {
	j := scriptJob("build", "echo hi")
	j.Steps[0].Env = []ir.Var{{Name: "TOK", Value: ir.Value{Kind: ir.ValueSecret, Secret: refName}}}
	files, _ := emit(t, project(j))
	back := roundTrip(t, files)
	again, _ := emit(t, back)
	if !strings.Contains(again[".argo/workflows/ci.yaml"], "secretKeyRef") {
		t.Errorf("the second trip lost the secret env:\n%s", again[".argo/workflows/ci.yaml"])
	}
}

func TestYAMLBooleanLookalikesLintClean(t *testing.T) {
	linttool.RequireArgo(t)
	j := scriptJob("build", "echo hi")
	j.Steps[0].Env = []ir.Var{{Name: "GO111MODULE", Value: ir.Value{Lit: "on"}}, {Name: "FLAG", Value: ir.Value{Lit: "no"}}, {Name: "V", Value: ir.Value{Lit: "1:30"}}}
	files, _ := emit(t, project(j))
	path := ".argo/workflows/ci.yaml"
	if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
	}
	if !strings.Contains(files[path], `value: "on"`) {
		t.Errorf("on is not quoted:\n%s", files[path])
	}
}

func matrixJob(id string) *ir.Job {
	j := scriptJob(id, "echo ${{ matrix.v }}")
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "v", Values: []string{"a", "b"}}}}
	return j
}

func TestDisabledMatrixJobStaysLintValid(t *testing.T) {
	linttool.RequireArgo(t)
	j := matrixJob("test")
	j.If = expr.Opaque{Dialect: "gha", Text: "github.event.foo == 'x'"}
	files, _ := emit(t, project(j))
	path := ".argo/workflows/ci.yaml"
	if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
	}
	if !strings.Contains(files[path], "{{= false }}") {
		t.Errorf("the job is not disabled:\n%s", files[path])
	}
}
