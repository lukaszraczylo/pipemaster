package argo_test

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"go.yaml.in/yaml/v3"
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

func envProject() *ir.Project {
	a := &ir.Job{ID: "a", Env: []ir.Var{{Name: "JOBVAR", Value: ir.Value{Lit: "j"}}}, Steps: []*ir.Step{
		{ID: "one", Kind: ir.StepScript, Script: []string{"echo 1"}, Env: []ir.Var{{Name: "STEPVAR", Value: ir.Value{Lit: "s"}}}},
		{ID: "two", Kind: ir.StepScript, Script: []string{"echo 2"}},
	}}
	b := scriptJob("b", "echo 3")
	p := project(a, b)
	p.Pipelines[0].Env = []ir.Var{{Name: "GO111MODULE", Value: ir.Value{Lit: "on"}}, {Name: "OTHER", Value: ir.Value{Lit: "x"}}}
	return p
}

func TestPipelineAndJobEnvComeBackWhereTheyWere(t *testing.T) {
	p := envProject()
	files, _ := emit(t, p)
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("the env moved:\n%s", diff)
	}
}

func TestEditedEnvValueKeepsTheStepsAndReportsTheMarker(t *testing.T) {
	files, _ := emit(t, envProject())
	path := ".argo/workflows/ci.yaml"
	files[path] = strings.Replace(files[path], "value: x", "value: edited", 1)
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries(rep, "marker ignored")) == 0 {
		t.Error("no marker ignored entry")
	}
	edited := 0
	for _, j := range back.Pipelines[0].Jobs {
		for _, st := range j.Steps {
			for _, v := range st.Env {
				if v.Name == "OTHER" && v.Value.Lit == "edited" {
					edited++
				}
			}
		}
	}
	if edited != 1 {
		t.Errorf("%d steps hold the edited value, want 1", edited)
	}
	for _, v := range back.Pipelines[0].Env {
		if v.Name == "OTHER" {
			t.Error("OTHER was lifted to the pipeline although the steps differ")
		}
	}
}

func TestCheckoutPathIsStableAcrossArgoTrips(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{
		actionStep("co", "actions", "checkout", map[string]string{"path": "."}),
		{ID: "run", Kind: ir.StepScript, Script: []string{"ls"}},
	}}
	first, _ := emit(t, project(j))
	back := roundTrip(t, first)
	second, _ := emit(t, back)
	path := ".argo/workflows/ci.yaml"
	if strings.Contains(second[path], "/workspace/workspace") {
		t.Errorf("the checkout path drifted:\n%s", second[path])
	}
	if !strings.Contains(first[path], "path: /workspace/.\n") {
		t.Errorf("first trip changed:\n%s", first[path])
	}
	back2 := roundTrip(t, second)
	stripExt(back)
	stripExt(back2)
	if ok, diff := ir.Equal(back, back2); !ok {
		t.Errorf("the second trip changed the IR:\n%s", diff)
	}
}

func TestPipelineWithNoJobsEmitsAValidPlaceholder(t *testing.T) {
	linttool.RequireArgo(t)
	p := project()
	files, rep := emit(t, p)
	path := ".argo/workflows/ci.yaml"
	if out, err := linttool.ArgoLint(t, path, []byte(files[path])); err != nil {
		t.Fatalf("argo lint: %v\n%s\n%s", err, out, files[path])
	}
	if len(entries(rep, "no job")) == 0 {
		t.Errorf("no report entry for the empty pipeline: %v", rep.Entries)
	}
}

func TestScheduleAndManualRunsReportEmptyContext(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{actionStep("co", "actions", "checkout", nil), {ID: "run", Kind: ir.StepScript, Script: []string{"ls"}}}}
	p := project(j)
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"}, {Kind: ir.TriggerManual}}
	_, rep := emit(t, p)
	n := 0
	for _, e := range entries(rep, "no value for ctx-repo, ctx-sha") {
		if e.Level == caps.External {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d empty-context entries, want 2 (cron and manual): %v", n, rep.Entries)
	}
}

func TestEditedGateKeepsTheFileAndReportsTheMarker(t *testing.T) {
	a, b := scriptJob("a", "x"), scriptJob("b", "x")
	b.Needs = []ir.Need{{JobID: "a"}}
	b.Manual = true
	files, _ := emit(t, project(a, b))
	path := ".argo/workflows/ci.yaml"
	var head []string
	for _, l := range strings.Split(files[path], "\n") {
		if strings.HasPrefix(l, "# pipemaster:") {
			head = append(head, l)
		}
	}
	d := docs(t, files[path])[0]
	// The user deletes the suspend task and its template, and points b at a.
	var tmpls []any
	for _, tm := range list(get(d, "spec", "templates")) {
		m := tm.(doc)
		if m["name"] == "b-gate" {
			continue
		}
		if m["name"] == "main" {
			var kept []any
			for _, tk := range list(get(m, "dag", "tasks")) {
				task := tk.(doc)
				if task["name"] == "b-gate" {
					continue
				}
				if task["name"] == "b" {
					task["dependencies"] = []any{"a"}
				}
				kept = append(kept, task)
			}
			m["dag"].(doc)["tasks"] = kept
		}
		tmpls = append(tmpls, m)
	}
	d["spec"].(doc)["templates"] = tmpls
	out, err := yaml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	files[path] = strings.Join(head, "\n") + "\n" + string(out)
	back, rep, err := parseRepo(files)
	if err != nil {
		t.Fatalf("%v\n%s", err, files[path])
	}
	if len(entries(rep, "marker ignored")) == 0 {
		t.Errorf("no marker ignored entry:\n%s", files[path])
	}
	for _, j := range back.Pipelines[0].Jobs {
		if j.Manual {
			t.Errorf("job %s is manual although the gate is gone", j.ID)
		}
	}
}
