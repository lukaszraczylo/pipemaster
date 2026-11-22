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

// artifactChain: two jobs upload a file named artifact; a third job needs both with artifacts.
func artifactChain() *ir.Project {
	up := func(id string) *ir.Job {
		j := scriptJob(id, "echo hi")
		j.Steps = append(j.Steps, actionStep(id+"-up", "actions", "upload-artifact", map[string]string{"name": "artifact", "path": id + "/out"}))
		return j
	}
	sink := scriptJob("sink", "ls")
	sink.Needs = []ir.Need{{JobID: "a", Artifacts: true}, {JobID: "b", Artifacts: true}}
	return project(up("a"), up("b"), sink)
}

func TestArtifactNamesAndPathsDoNotCollide(t *testing.T) {
	linttool.RequireArgo(t)
	files, _ := emit(t, artifactChain())
	path := ".argo/workflows/ci.yaml"
	if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
	}
	text := files[path]
	if n := strings.Count(text, "path: /workspace/artifacts/artifact\n"); n != 1 {
		t.Errorf("%d inputs write to /workspace/artifacts/artifact, want 1:\n%s", n, text)
	}
}

func TestOutputArtifactKeepsItsNameBesideAnInput(t *testing.T) {
	up := scriptJob("a", "echo")
	up.Steps = append(up.Steps, actionStep("a-up", "actions", "upload-artifact", map[string]string{"name": "artifact", "path": "x"}))
	mid := scriptJob("mid", "echo")
	mid.Needs = []ir.Need{{JobID: "a", Artifacts: true}}
	mid.Steps = append(mid.Steps, actionStep("m-up", "actions", "upload-artifact", map[string]string{"name": "artifact", "path": "y"}))
	sink := scriptJob("sink", "echo")
	sink.Needs = []ir.Need{{JobID: "mid", Artifacts: true}}
	files, _ := emit(t, project(up, mid, sink))
	text := files[".argo/workflows/ci.yaml"]
	if !strings.Contains(text, "{{tasks.mid.outputs.artifacts.artifact}}") || strings.Contains(text, "artifact-3") {
		t.Errorf("the output of mid was renamed:\n%s", text)
	}
}

func TestMatrixRefWithoutAMatrixIsLostAndLintValid(t *testing.T) {
	linttool.RequireArgo(t)
	j := scriptJob("compare", "echo ${{ matrix.mode }}")
	j.Steps[0].If = eqStr([]string{"matrix", "mode"}, "x")
	j.Env = []ir.Var{{Name: "M", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{"matrix", "mode"}}}}}
	files, rep := emit(t, project(j))
	path := ".argo/workflows/ci.yaml"
	if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
	}
	if len(entries(rep, "matrix")) == 0 {
		t.Error("no report entry for the unbound matrix reference")
	}
}

func TestGoTemplateTextInAScriptReadsBack(t *testing.T) {
	line := `docker inspect --format '{{.State.Status}}' x; echo '{{ .Values }}'`
	files, _ := emit(t, project(scriptJob("build", line)))
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, joinFiles(files))
	}
	if len(back.Pipelines) != 1 || len(back.Pipelines[0].Jobs) != 1 {
		t.Fatalf("the file was skipped as Helm text: %v\n%s", rep.Entries, joinFiles(files))
	}
	if got := strings.Join(back.Pipelines[0].Jobs[0].Steps[0].Script, "\n"); !strings.Contains(got, "{{.State.Status}}") {
		t.Errorf("script = %q", got)
	}
}

func TestTwoCheckoutsGetDistinctStepIDs(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{
		actionStep("co1", "actions", "checkout", nil),
		actionStep("co2", "actions", "checkout", map[string]string{"repository": "o/other", "path": "other"}),
		{ID: "run", Kind: ir.StepScript, Script: []string{"ls"}},
	}}
	files, _ := emit(t, project(j))
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, joinFiles(files))
	}
	seen := map[string]bool{}
	for _, st := range back.Pipelines[0].Jobs[0].Steps {
		if seen[st.ID] {
			t.Errorf("duplicate step id %s", st.ID)
		}
		seen[st.ID] = true
	}
}
