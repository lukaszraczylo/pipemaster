package tekton_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

func TestPipelineIDAndInputs(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  displayName: CI
  params:
  - {name: version, type: string, default: "1.0", description: the version}
  - {name: required-one}
  - {name: ctx-branch, default: ""}
  - {name: ctx-pr-number}
`+inlineTask))
	if pl.ID != "ci" || pl.Name != "CI" {
		t.Fatalf("id=%q name=%q", pl.ID, pl.Name)
	}
	want := []ir.Input{
		{Name: "version", Type: "string", Default: "1.0", Description: "the version"},
		{Name: "required-one", Required: true},
	}
	if !reflect.DeepEqual(pl.Inputs, want) {
		t.Fatalf("inputs = %+v, want %+v", pl.Inputs, want)
	}
}

func TestRunAfterToNeeds(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
  - name: b
    runAfter: [a]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
`))
	if got := job(t, pl, "b").Needs; !reflect.DeepEqual(got, []ir.Need{{JobID: "a"}}) {
		t.Fatalf("needs = %+v", got)
	}
	if e := ext(job(t, pl, "b").Ext); e != nil && e[tekton.ExtImplicitNeeds] != nil {
		t.Fatalf("runAfter need must not be implicit: %v", e)
	}
}

func TestResultRefAddsImplicitNeed(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec: {results: [{name: out}], steps: [{name: s, image: x, script: "true"}]}
  - name: b
    params:
    - {name: v, value: $(tasks.a.results.out)}
    taskSpec:
      params: [{name: v}]
      steps: [{name: s, image: x, script: "echo $(params.v)"}]
`))
	b := job(t, pl, "b")
	if !reflect.DeepEqual(b.Needs, []ir.Need{{JobID: "a"}}) {
		t.Fatalf("needs = %+v", b.Needs)
	}
	if got := ext(b.Ext)[tekton.ExtImplicitNeeds]; !reflect.DeepEqual(got, []any{"a"}) {
		t.Fatalf("implicit needs = %v", got)
	}
	if got := step(t, b, "s").Script; !reflect.DeepEqual(got, []string{"echo ${{ needs.a.outputs.out }}"}) {
		t.Fatalf("script = %q", got)
	}
	if o := job(t, pl, "a").Outputs; len(o) != 1 || o[0].Name != "out" {
		t.Fatalf("outputs = %+v", o)
	}
}

func TestRunAfterAndResultRefIsExplicit(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec: {results: [{name: out}], steps: [{name: s, image: x, script: "true"}]}
  - name: b
    runAfter: [a]
    when:
    - {input: "$(tasks.a.results.out)", operator: in, values: [ok]}
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
`))
	b := job(t, pl, "b")
	if len(b.Needs) != 1 || ext(b.Ext)[tekton.ExtImplicitNeeds] != nil {
		t.Fatalf("needs=%+v ext=%v", b.Needs, b.Ext)
	}
}

func TestWhenBecomesCondition(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    when:
    - {input: "$(params.ctx-branch)", operator: in, values: [main, dev]}
    - {input: "$(params.mode)", operator: notin, values: [fast]}
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
  - name: b
    when:
    - cel: "'$(params.ctx-branch)'.matches('release/.*')"
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
`))
	if got, want := cond(job(t, pl, "a").If), "((branch == 'main') || (branch == 'dev')) && (inputs.mode != 'fast')"; got != want {
		t.Fatalf("if = %q, want %q", got, want)
	}
	if !strings.Contains(cond(job(t, pl, "b").If), "opaque(tekton)") {
		t.Fatalf("cel when must be opaque: %q", cond(job(t, pl, "b").If))
	}
	if !hasEntry(rep, "cel") {
		t.Fatal("a cel when needs a lost entry")
	}
}

func TestRetriesTimeoutOnError(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    retries: 2
    timeout: 1h30m
    onError: continue
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
`))
	j := job(t, pl, "a")
	if j.Retry == nil || j.Retry.Attempts != 3 {
		t.Fatalf("retry = %+v", j.Retry)
	}
	if j.Timeout == nil || time.Duration(*j.Timeout) != 90*time.Minute {
		t.Fatalf("timeout = %v", j.Timeout)
	}
	if !j.AllowFailure {
		t.Fatal("onError: continue must set AllowFailure")
	}
}

func TestMatrix(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    matrix:
      params:
      - {name: go, value: ["1.22", "1.23"]}
      - {name: os, value: [linux]}
      include:
      - name: extra
        params:
        - {name: go, value: "1.24"}
        - {name: os, value: windows}
    taskSpec:
      params: [{name: go}, {name: os}]
      steps: [{name: s, image: x, script: "echo $(params.go) $(params.os)"}]
`))
	j := job(t, pl, "a")
	want := &ir.Matrix{
		Axes:    []ir.Axis{{Name: "go", Values: []string{"1.22", "1.23"}}, {Name: "os", Values: []string{"linux"}}},
		Include: []ir.Combo{{{Key: "go", Value: "1.24"}, {Key: "os", Value: "windows"}}},
	}
	if !reflect.DeepEqual(j.Matrix, want) {
		t.Fatalf("matrix = %+v", j.Matrix)
	}
	if got := step(t, j, "s").Script; !reflect.DeepEqual(got, []string{"echo ${{ matrix.go }} ${{ matrix.os }}"}) {
		t.Fatalf("script = %q", got)
	}
}

func TestFinallyStatusConditions(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(inlineTask+`  finally:
  - name: notify
    when: [{input: "$(tasks.status)", operator: in, values: [Failed]}]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
  - name: report
    when: [{input: "$(tasks.status)", operator: in, values: [Succeeded]}]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
  - name: not-failed
    when: [{input: "$(tasks.status)", operator: notin, values: [Failed]}]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
  - name: build-failed
    when: [{input: "$(tasks.build.status)", operator: in, values: [Failed]}]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
  - name: odd
    when: [{input: "$(tasks.status)", operator: in, values: [Completed]}]
    taskSpec: {steps: [{name: s, image: x, script: "true"}]}
`))
	for id, want := range map[string]string{
		"notify": "failure()", "report": "success()", "not-failed": "!failure()",
		"build-failed": "needs.build.result == 'failure'",
	} {
		if got := cond(job(t, pl, id).If); got != want {
			t.Errorf("%s: if = %q, want %q", id, got, want)
		}
	}
	if !strings.Contains(cond(job(t, pl, "odd").If), "opaque(tekton)") || !hasEntry(rep, "Completed") {
		t.Errorf("an unmapped status stays opaque with an entry: %q", cond(job(t, pl, "odd").If))
	}
	if len(pl.Finally) != 5 || len(pl.Jobs) != 1 {
		t.Fatalf("jobs=%d finally=%d", len(pl.Jobs), len(pl.Finally))
	}
	if got := job(t, pl, "build-failed").Needs; !reflect.DeepEqual(got, []ir.Need{{JobID: "build"}}) {
		t.Fatalf("needs = %+v", got)
	}
}

func TestMetadataDisplayNameDescription(t *testing.T) {
	pl, rep := parseYAML(t, `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
  labels: {app: x}
  annotations: {note: y}
spec:
  description: whole pipeline
  workspaces: [{name: src}]
  results: [{name: r, value: $(tasks.a.results.o)}]
  tasks:
  - name: a
    displayName: Build it
    description: does build
    workspaces: [{name: src, workspace: src}]
    taskSpec:
      workspaces: [{name: src}]
      steps: [{name: s, image: x, script: "true"}]
`)
	e := ext(pl.Ext)
	for _, k := range []string{"metadata", "description", "workspaces", "results"} {
		if e[k] == nil {
			t.Errorf("pipeline Ext[tekton][%s] missing: %v", k, e)
		}
	}
	j := job(t, pl, "a")
	if j.Name != "Build it" || ext(j.Ext)["description"] != "does build" || ext(j.Ext)["workspaces"] == nil || ext(j.Ext)["task_workspaces"] == nil {
		t.Fatalf("job = %+v ext=%v", j, j.Ext)
	}
	if !hasEntry(rep, "workspaces") {
		t.Fatal("workspaces need an entry")
	}
}

func TestMultiDocumentFileAndV1Beta1(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		"all.yaml": `apiVersion: tekton.dev/v1beta1
kind: Task
metadata: {name: unit}
spec:
  steps: [{name: t, image: golang, script: go test}]
---
apiVersion: tekton.dev/v1beta1
kind: Pipeline
metadata: {name: ci}
spec:
  tasks:
  - {name: test, taskRef: {name: unit}}
`})
	if len(p.Pipelines) != 1 || p.Pipelines[0].Jobs[0].Steps[0].Script[0] != "go test" {
		t.Fatalf("project = %+v", p.Pipelines[0].Jobs[0])
	}
	if p.Origin.Provider != "tekton" || !reflect.DeepEqual(p.Origin.Files, []string{".tekton/all.yaml"}) {
		t.Fatalf("origin = %+v", p.Origin)
	}
}

func TestInvalidInput(t *testing.T) {
	parseErr(t, "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata: {name: ci}\nspec: {}\n")
	parseErr(t, "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata: {name: ci}\nspec:\n  tasks:\n  - {name: a, runAfter: [ghost], taskSpec: {steps: [{name: s, image: x, script: x}]}}\n")
	parseErr(t, "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata: {name: ci}\nspec:\n  tasks: [\n")
}
