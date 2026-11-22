package argo_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func one(t *testing.T, tasks, templates string) (*ir.Job, *report.Report) {
	t.Helper()
	pl, rep := parseYAML(t, dagOf(tasks, templates))
	return pl.Jobs[0], rep
}

func TestContainerTemplateIsOneStepWithACommandLine(t *testing.T) {
	j, _ := one(t, "      - {name: a, template: t}\n", `  - name: t
    container:
      image: golang:1.27
      command: [go]
      args: [test, "./..."]
      workingDir: /src
      env:
      - {name: CGO_ENABLED, value: "0"}
`)
	if j.Container == nil || j.Container.Ref != "golang:1.27" || len(j.Steps) != 1 {
		t.Fatalf("job %+v", j)
	}
	st := j.Steps[0]
	if st.ID != "main" || st.Kind != ir.StepScript || !reflect.DeepEqual(st.Script, []string{"go test ./..."}) || st.WorkDir != "/src" {
		t.Fatalf("step %+v", st)
	}
	if len(st.Env) != 1 || st.Env[0].Name != "CGO_ENABLED" || st.Env[0].Value.Lit != "0" {
		t.Fatalf("env %+v", st.Env)
	}
	e := ext(st.Ext)
	if e["command"] == nil || e["args"] == nil || e["script_line"] != "go test ./..." {
		t.Fatalf("marker keys %v", e)
	}
}

func TestScriptTemplate(t *testing.T) {
	j, _ := one(t, "      - {name: a, template: t}\n", `  - name: t
    script:
      image: python:3
      command: [python]
      source: |
        print("hi")
        print("{{inputs.parameters.who}}")
`)
	st := j.Steps[0]
	if st.Shell != "python" || len(st.Script) != 2 || st.Script[0] != `print("hi")` {
		t.Fatalf("step %+v", st)
	}
	if !strings.Contains(st.Script[1], "who") {
		t.Fatalf("an unbound input vanished: %q", st.Script[1])
	}
}

func TestContainerSetStepsFollowDependencies(t *testing.T) {
	j, rep := one(t, "      - {name: a, template: t}\n", `  - name: t
    containerSet:
      retryStrategy: {retries: "2"}
      containers:
      - {name: test, image: go, command: [go, test], dependencies: [build]}
      - {name: build, image: go, command: [go, build]}
      - {name: main, image: go, command: [echo, done], dependencies: [test]}
`)
	if got := stepIDs(j); !reflect.DeepEqual(got, []string{"build", "test", "main"}) {
		t.Fatalf("steps %v", got)
	}
	if j.Container == nil || j.Container.Ref != "go" || j.Steps[0].Image != nil {
		t.Fatalf("common image not lifted: %+v", j.Container)
	}
	if ext(j.Ext)["containerSet"] == nil || !hasEntry(rep, "containerSet") {
		t.Fatalf("containerSet keys lost: %v", ext(j.Ext))
	}
}

func TestContainerSetThatIsNotAChainKeepsItsGraph(t *testing.T) {
	j, rep := one(t, "      - {name: a, template: t}\n", `  - name: t
    containerSet:
      containers:
      - {name: a, image: x, command: [a]}
      - {name: b, image: y, command: [b]}
      - {name: main, image: x, command: [m], dependencies: [a, b]}
`)
	if got := stepIDs(j); !reflect.DeepEqual(got, []string{"a", "b", "main"}) {
		t.Fatalf("steps %v", got)
	}
	if j.Container != nil || j.Steps[1].Image == nil || j.Steps[1].Image.Ref != "y" {
		t.Fatalf("per-step images: container %v step %+v", j.Container, j.Steps[1].Image)
	}
	if ext(j.Steps[2].Ext)["dependencies"] == nil || !hasEntry(rep, "not a chain") {
		t.Fatalf("graph lost: %v %+v", ext(j.Steps[2].Ext), rep.Entries)
	}
}

func TestContainerSetCycleIsAnError(t *testing.T) {
	parseErr(t, dagOf("      - {name: a, template: t}\n", `  - name: t
    containerSet:
      containers:
      - {name: x, image: i, dependencies: [y]}
      - {name: y, image: i, dependencies: [x]}
`))
}

func stepIDs(j *ir.Job) []string {
	var out []string
	for _, s := range j.Steps {
		out = append(out, s.ID)
	}
	return out
}

func TestOtherTemplateTypesAreTaskSteps(t *testing.T) {
	for kind, body := range map[string]string{
		"resource": "resource: {action: create, manifest: \"kind: ConfigMap\"}",
		"http":     "http: {url: \"https://example.com\"}",
		"plugin":   "plugin: {hello: {}}",
		"data":     "data: {source: {artifactPaths: {name: a}}, transformation: [{expression: x}]}",
		"dag":      "dag: {tasks: [{name: inner, template: echo}]}",
	} {
		t.Run(kind, func(t *testing.T) {
			pl, rep := parseYAML(t, dagOf("      - {name: a, template: t}\n", echoTemplates+"  - name: t\n    "+body+"\n"))
			j := pl.Jobs[0]
			if len(j.Steps) != 1 || j.Steps[0].Kind != ir.StepTask {
				t.Fatalf("steps %+v", j.Steps)
			}
			tpl, _ := ext(j.Steps[0].Ext)["template"].(map[string]any)
			if tpl == nil || tpl[kind] == nil || !hasEntry(rep, kind+" template") {
				t.Fatalf("template not kept: %v %+v", ext(j.Steps[0].Ext), rep.Entries)
			}
		})
	}
}

func TestRetryStrategy(t *testing.T) {
	for limit, want := range map[string]int{"3": 4, `"2"`: 3} {
		j, _ := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    retryStrategy: {limit: "+limit+", retryPolicy: Always, backoff: {duration: 5s}}\n")
		if j.Retry == nil || j.Retry.Attempts != want {
			t.Fatalf("limit %s: Retry %+v", limit, j.Retry)
		}
		rs, _ := ext(j.Ext)["retryStrategy"].(map[string]any)
		if rs["retryPolicy"] != "Always" || rs["backoff"] == nil || rs["limit"] != nil {
			t.Fatalf("retryStrategy rest = %v", rs)
		}
	}
	for _, limit := range []string{"0", `"0"`} {
		j, _ := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    retryStrategy: {limit: "+limit+"}\n")
		if j.Retry == nil || j.Retry.Attempts != 1 {
			t.Fatalf("limit %s: Retry %+v, want one attempt", limit, j.Retry)
		}
	}
	if j, _ := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n"); j.Retry != nil {
		t.Fatalf("no retryStrategy: Retry %+v, want nil", j.Retry)
	}
	j, rep := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    retryStrategy: {retryPolicy: Always}\n")
	if j.Retry != nil || ext(j.Ext)["retryStrategy"] == nil || !hasEntry(rep, "retryStrategy") {
		t.Fatalf("limitless strategy: %+v %v", j.Retry, ext(j.Ext))
	}
}

func TestTimeouts(t *testing.T) {
	j, _ := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    timeout: 30m\n")
	if j.Timeout == nil || time.Duration(*j.Timeout) != 30*time.Minute {
		t.Fatalf("timeout %v", j.Timeout)
	}
	j, _ = one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    activeDeadlineSeconds: 90\n")
	if j.Timeout == nil || time.Duration(*j.Timeout) != 90*time.Second {
		t.Fatalf("deadline %v", j.Timeout)
	}
	j, rep := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    timeout: soon\n")
	if j.Timeout != nil || ext(j.Ext)["timeout"] != "soon" || !hasEntry(rep, "timeout") {
		t.Fatalf("bad timeout: %v %v", j.Timeout, ext(j.Ext))
	}
}

func TestSidecarsAreServices(t *testing.T) {
	j, _ := one(t, "      - {name: a, template: t}\n", `  - name: t
    container: {image: x}
    sidecars:
    - name: db
      image: postgres:16
      env: [{name: POSTGRES_PASSWORD, value: pw}]
      ports: [{containerPort: 5432}]
      resources: {limits: {memory: 1Gi}}
`)
	if len(j.Services) != 1 || j.Services[0].Name != "db" || j.Services[0].Image.Ref != "postgres:16" || !reflect.DeepEqual(j.Services[0].Ports, []string{"5432"}) {
		t.Fatalf("services %+v", j.Services)
	}
	sc, _ := ext(j.Ext)["sidecars"].(map[string]any)
	if sc["db"] == nil {
		t.Fatalf("sidecar keys lost: %v", ext(j.Ext))
	}
}

func TestOutputsAndArtifacts(t *testing.T) {
	pl, rep := parseYAML(t, dagOf(`      - {name: build, template: build}
      - name: pack
        template: pack
        dependencies: [build]
        arguments:
          artifacts: [{name: bin, from: "{{tasks.build.outputs.artifacts.bin}}"}]
`, echoTemplates+`  - name: build
    container: {image: x}
    outputs:
      parameters: [{name: version, valueFrom: {path: /tmp/v}}, {name: plain}]
      artifacts: [{name: bin, path: /out/bin, s3: {key: k}}]
  - name: pack
    inputs:
      artifacts: [{name: bin, path: /in/bin}]
    container: {image: x}
`))
	b := job(t, pl, "build")
	if !reflect.DeepEqual(b.Outputs, []ir.Output{{Name: "version"}, {Name: "plain"}}) {
		t.Fatalf("outputs %+v", b.Outputs)
	}
	if !reflect.DeepEqual(b.Artifacts, []ir.Artifact{{Name: "bin", Paths: []string{"/out/bin"}}}) {
		t.Fatalf("artifacts %+v", b.Artifacts)
	}
	if ext(b.Ext)["outputs"] == nil || !hasEntry(rep, "outputs") {
		t.Fatalf("output keys lost: %v", ext(b.Ext))
	}
	p := job(t, pl, "pack")
	if len(p.Needs) != 1 || !p.Needs[0].Artifacts || p.Needs[0].JobID != "build" {
		t.Fatalf("pack needs %+v", p.Needs)
	}
	if ext(p.Ext)["inputs"] == nil {
		t.Fatalf("input artifact detail lost: %v", ext(p.Ext))
	}
}

func TestGitInputArtifactIsACheckoutStep(t *testing.T) {
	j, _ := one(t, "      - {name: a, template: t}\n", `  - name: t
    inputs:
      artifacts:
      - name: src
        path: /src
        git: {repo: "https://github.com/acme/app.git", revision: v1.2, depth: 1}
    container: {image: x, command: [ls]}
`)
	if len(j.Steps) != 2 || j.Steps[0].Kind != ir.StepAction || j.Steps[0].Action == nil || j.Steps[0].Action.Repo != "checkout" {
		t.Fatalf("steps %+v", j.Steps)
	}
	w := j.Steps[0].With
	if w["repository"].Lit != "acme/app" || w["ref"].Lit != "v1.2" || w["fetch-depth"].Lit != "1" || w["path"].Lit != "/src" {
		t.Fatalf("with %+v", w)
	}
}

func TestGitInputWithAnotherHostStaysATaskStep(t *testing.T) {
	j, rep := one(t, "      - {name: a, template: t}\n", `  - name: t
    inputs:
      artifacts:
      - {name: src, path: /src, git: {repo: "https://git.example.com/a/b.git", usernameSecret: {name: s, key: u}}}
    container: {image: x}
`)
	if j.Steps[0].Kind != ir.StepTask || ext(j.Steps[0].Ext)["artifact"] == nil || !hasEntry(rep, "git artifact") {
		t.Fatalf("steps %+v %v", j.Steps[0], ext(j.Steps[0].Ext))
	}
}

func TestNodeSelectorPlatform(t *testing.T) {
	j, _ := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    nodeSelector: {kubernetes.io/os: linux, kubernetes.io/arch: arm64}\n")
	if !reflect.DeepEqual(j.Runner.Labels, []string{"linux/arm64"}) || ext(j.Ext)["nodeSelector"] != nil {
		t.Fatalf("runner %+v ext %v", j.Runner, ext(j.Ext))
	}
	j, rep := one(t, "      - {name: a, template: t}\n", "  - name: t\n    container: {image: x}\n    nodeSelector: {kubernetes.io/os: linux, gpu: \"true\"}\n")
	if len(j.Runner.Labels) != 0 || ext(j.Ext)["nodeSelector"] == nil || !hasEntry(rep, "nodeSelector") {
		t.Fatalf("partial selector: %+v %v", j.Runner, ext(j.Ext))
	}
}

func TestTemplateParamDescriptionsAreKept(t *testing.T) {
	pl, rep := parseYAML(t, dagOf("      - {name: a, template: t, arguments: {parameters: [{name: msg, value: hi}]}}\n", `  - name: t
    inputs:
      parameters:
      - {name: msg, description: The message to print}
    container: {image: x, command: [echo, "{{inputs.parameters.msg}}"]}
`))
	j := pl.Jobs[0]
	in, _ := ext(j.Ext)["inputs"].(map[string]any)
	if !strings.Contains(fmtAny(in), "The message to print") || !hasEntry(rep, "description") {
		t.Fatalf("description lost: %v %+v", ext(j.Ext), rep.Entries)
	}
	if got := j.Steps[0].Script[0]; got != "echo hi" {
		t.Fatalf("bound argument not substituted: %q", got)
	}
}

func TestBoundArgumentsReachTheScript(t *testing.T) {
	pl, _ := parseYAML(t, dagOf(`      - {name: build, template: build}
      - name: show
        template: show
        dependencies: [build]
        arguments:
          parameters:
          - {name: branch, value: "{{workflow.parameters.ctx-branch}}"}
          - {name: ver, value: "{{tasks.build.outputs.parameters.version}}"}
          - {name: unused, value: x}
`, `  - {name: build, container: {image: x}, outputs: {parameters: [{name: version, valueFrom: {path: /v}}]}}
  - name: show
    inputs: {parameters: [{name: branch}, {name: ver}, {name: unused}]}
    script:
      image: x
      source: echo "{{inputs.parameters.branch}} {{inputs.parameters.ver}} {{workflow.name}}"
`))
	s := job(t, pl, "show").Steps[0].Script
	want := `echo "${{ branch }} ${{ needs.build.outputs.version }} {{workflow.name}}"`
	if len(s) != 1 || s[0] != want {
		t.Fatalf("script %q, want %q", s, want)
	}
	args, _ := ext(job(t, pl, "show").Ext)["arguments"].(map[string]any)
	if args["unused"] == nil || args["branch"] != nil {
		t.Fatalf("unread arguments = %v", args)
	}
}

func TestTemplateRefIsResolvedInTheSameRepo(t *testing.T) {
	lib := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: lib}\nspec:\n  templates:\n  - {name: lint, container: {image: l, command: [lint]}}\n"
	main := dagOf("      - {name: a, templateRef: {name: lib, template: lint}}\n      - {name: b, templateRef: {name: elsewhere, template: x}}\n", "")
	p, rep := parseFiles(t, map[string]string{"lib.yaml": lib, "ci.yaml": main})
	var ci *ir.Pipeline
	for _, pl := range p.Pipelines {
		if pl.ID == "ci" {
			ci = pl
		}
	}
	a, b := job(t, ci, "a"), job(t, ci, "b")
	if a.Container == nil || a.Container.Ref != "l" {
		t.Fatalf("a %+v", a)
	}
	if b.Steps[0].Kind != ir.StepTask || ext(b.Ext)["templateRef"] == nil || !hasEntry(rep, "templateRef") {
		t.Fatalf("b %+v %v", b.Steps, ext(b.Ext))
	}
}

func TestInlineTemplate(t *testing.T) {
	j, _ := one(t, "      - name: a\n        inline:\n          container: {image: i, command: [x]}\n", "")
	if j.Container == nil || j.Container.Ref != "i" {
		t.Fatalf("job %+v", j)
	}
}
