package tekton_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

func TestInlineTaskSpecSteps(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: build
    taskSpec:
      steps:
      - name: one
        displayName: Step one
        image: golang:1.23
        workingDir: /src
        timeout: 5m
        onError: continue
        env:
        - {name: A, value: "1"}
        - {name: B, value: "$(params.ctx-branch)"}
        script: |
          #!/bin/bash -e
          echo hi
          echo $(date)
      - name: two
        image: alpine
        command: [echo]
        args: [hello world, "$(params.ctx-sha)"]
`))
	j := job(t, pl, "build")
	one := step(t, j, "one")
	if one.Kind != ir.StepScript || one.Name != "Step one" || one.Image == nil || one.Image.Ref != "golang:1.23" || one.WorkDir != "/src" {
		t.Fatalf("step one = %+v", one)
	}
	if one.Shell != "/bin/bash -e" || !reflect.DeepEqual(one.Script, []string{"echo hi", "echo $(date)"}) {
		t.Fatalf("shell=%q script=%q", one.Shell, one.Script)
	}
	if one.Timeout == nil || time.Duration(*one.Timeout) != 5*time.Minute || !one.ContinueOnError {
		t.Fatalf("timeout/onError: %+v", one)
	}
	if len(one.Env) != 2 || one.Env[0].Value.Lit != "1" || one.Env[1].Value.Kind != ir.ValueExpr || cond(one.Env[1].Value.Expr) != "branch" {
		t.Fatalf("env = %+v", one.Env)
	}
	two := step(t, j, "two")
	if two.Kind != ir.StepScript || !reflect.DeepEqual(two.Script, []string{`echo 'hello world' "${{ sha }}"`}) {
		t.Fatalf("two = %+v", two)
	}
	e := ext(two.Ext)
	if !reflect.DeepEqual(e[tekton.ExtCommand], []any{"echo"}) || !reflect.DeepEqual(e[tekton.ExtArgs], []any{"hello world", "$(params.ctx-sha)"}) {
		t.Fatalf("command marker = %v", e)
	}
}

func TestTaskParamDefaultAndStepTemplate(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"p.yaml": `apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: unit
  labels: {a: b}
spec:
  params: [{name: flags, default: -v}]
  results: [{name: cover, description: coverage}]
  workspaces: [{name: src}]
  stepTemplate:
    image: golang
    env: [{name: GOFLAGS, value: -mod=vendor}]
    resources: {limits: {cpu: "1"}}
  sidecars:
  - name: db
    image: postgres:16
    env: [{name: PW, value: x}]
    ports: [{containerPort: 5432}]
    readinessProbe: {exec: {command: [ok]}}
  steps:
  - name: t
    script: go test $(params.flags)
---
apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: ci}
spec:
  tasks:
  - {name: test, taskRef: {name: unit}}
`})
	j := p.Pipelines[0].Jobs[0]
	if j.Container == nil || j.Container.Ref != "golang" || len(j.Env) != 1 || j.Env[0].Name != "GOFLAGS" {
		t.Fatalf("container=%+v env=%+v", j.Container, j.Env)
	}
	if s := step(t, j, "t"); s.Image != nil || !reflect.DeepEqual(s.Script, []string{"go test -v"}) {
		t.Fatalf("step = %+v", s)
	}
	if len(j.Services) != 1 || j.Services[0].Name != "db" || j.Services[0].Image.Ref != "postgres:16" || !reflect.DeepEqual(j.Services[0].Ports, []string{"5432"}) {
		t.Fatalf("services = %+v", j.Services)
	}
	if len(j.Outputs) != 1 || j.Outputs[0].Name != "cover" {
		t.Fatalf("outputs = %+v", j.Outputs)
	}
	e := ext(j.Ext)
	if e["task_workspaces"] == nil || e["stepTemplate"] == nil || e["sidecars"] == nil || e["task_metadata"] == nil {
		t.Fatalf("ext = %v", e)
	}
	if !hasEntry(rep, "resources") {
		t.Fatal("stepTemplate.resources needs an entry")
	}
}

func TestRemoteTaskRefs(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(`  tasks:
  - name: clone
    taskRef:
      resolver: git
      params:
      - {name: url, value: https://example.com/r.git}
    params:
    - {name: url, value: https://example.com/app.git}
  - name: missing
    taskRef: {name: not-in-files}
  - name: bundled
    taskRef: {resolver: bundles, params: [{name: bundle, value: ghcr.io/x/y}]}
`))
	for _, id := range []string{"clone", "missing", "bundled"} {
		j := job(t, pl, id)
		if len(j.Steps) != 1 || j.Steps[0].Kind != ir.StepTask {
			t.Fatalf("%s steps = %+v", id, j.Steps)
		}
		if ext(j.Steps[0].Ext)["taskRef"] == nil {
			t.Fatalf("%s: taskRef missing from step Ext: %v", id, j.Steps[0].Ext)
		}
	}
	if ext(job(t, pl, "clone").Ext)["params"] == nil {
		t.Fatal("the params of a remote task stay in the job Ext")
	}
	if !hasEntry(rep, "remote") || !hasEntry(rep, "not-in-files") {
		t.Fatalf("entries = %+v", rep.Entries)
	}
}

func TestStepActionRef(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec:
      steps:
      - name: clone
        ref: {resolver: hub, params: [{name: name, value: git-clone}]}
        params: [{name: url, value: x}]
`))
	s := step(t, job(t, pl, "a"), "clone")
	if s.Kind != ir.StepAction || ext(s.Ext)["ref"] == nil || ext(s.Ext)["params"] == nil {
		t.Fatalf("step = %+v", s)
	}
	if !hasEntry(rep, "StepAction") {
		t.Fatal("a StepAction ref needs an entry")
	}
}

func TestGuardIsRestoredToStepCondition(t *testing.T) {
	cond1 := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"branch"}}, R: expr.Lit{Kind: expr.LitStr, Str: "main"}}
	lines, err := exprtekton.AddGuard([]string{"make"}, cond1)
	if err != nil {
		t.Fatal(err)
	}
	envs, ok := exprtekton.GuardEnv(cond1)
	if !ok {
		t.Fatal("GuardEnv")
	}
	script := "          #!/bin/sh\n"
	for _, l := range lines {
		script += "          " + l + "\n"
	}
	env := ""
	for _, e := range envs {
		env += "        - {name: " + e.Name + ", value: \"" + e.Value + "\"}\n"
	}
	pl, _ := parseYAML(t, pipelineWith("  tasks:\n  - name: a\n    taskSpec:\n      steps:\n      - name: s\n        image: x\n        env:\n"+env+"        script: |\n"+script))
	s := step(t, job(t, pl, "a"), "s")
	if cond(s.If) != "branch == 'main'" || !reflect.DeepEqual(s.Script, []string{"make"}) || len(s.Env) != 0 {
		t.Fatalf("step = %+v (if %q)", s, cond(s.If))
	}
}

func TestEscapedTemplateText(t *testing.T) {
	pl, _ := parseYAML(t, pipelineWith(`  tasks:
  - name: a
    taskSpec:
      steps:
      - {name: s, image: x, script: "echo '${{ x }}' $(context.taskRun.name) $(results.o.path)"}
`))
	got := step(t, job(t, pl, "a"), "s").Script
	if len(got) != 1 || got[0] != "echo '${{ '${{' }} x }}' $(context.taskRun.name) $(results.o.path)" {
		t.Fatalf("script = %q", got)
	}
}
