package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
	"github.com/lukaszraczylo/pipemaster/report"
)

// identityOf parses Tekton files, emits them and parses the result; it requires both parses to be equal.
func identityOf(t *testing.T, in map[string]string) (map[string]string, *report.Report) {
	t.Helper()
	p, _ := parseFiles(t, in)
	raw, rep, err := tekton.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	files := map[string]string{}
	for n, b := range raw {
		files[n] = string(b)
	}
	back, _ := reparse(t, files)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("identity broken:\n%s\n%s", diff, joinFiles(files))
	}
	return files, rep
}

func TestSecretRefSurvivesTektonRoundTrip(t *testing.T) {
	files, _ := identityOf(t, map[string]string{"pipeline.yaml": triggerFiles, "triggers.yaml": listener})
	src := joinFiles(files)
	for _, want := range []string{"secretName: s", "secretKey: k", "serviceAccountName: tekton-triggers", "gh-binding"} {
		if !strings.Contains(src, want) {
			t.Fatalf("%q missing:\n%s", want, src)
		}
	}
	if strings.Contains(src, "pipemaster-github-webhook") {
		t.Fatalf("a Tekton secretRef was replaced:\n%s", src)
	}
}

func TestForeignGitHubInterceptorCarriesSecretRef(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	files, rep := emit(t, p)
	if !strings.Contains(joinFiles(files), "secretName: pipemaster-github-webhook") {
		t.Fatalf("no secretRef:\n%s", joinFiles(files))
	}
	wantEntry(t, rep, caps.TriggerPush, caps.External, "pipemaster-github-webhook")
}

const workspacePipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: ci}
spec:
  workspaces: [{name: shared}]
  tasks:
  - name: a
    workspaces: [{name: shared, workspace: shared}]
    computeResources: {requests: {cpu: "1"}}
    taskSpec:
      workspaces: [{name: shared}]
      volumes: [{name: v, emptyDir: {}}]
      steps: [{name: s, image: x, script: "true"}]
`

func TestJobExtSurvivesTektonRoundTrip(t *testing.T) {
	files, _ := identityOf(t, map[string]string{"pipeline.yaml": workspacePipeline})
	pt := list(get(pipelineDoc(t, files, "ci"), "spec", "tasks"))[0].(doc)
	if len(list(pt["workspaces"])) != 1 || pt["computeResources"] == nil {
		t.Fatalf("PipelineTask lost keys: %v", pt)
	}
	if !strings.Contains(joinFiles(files), "emptyDir") {
		t.Fatalf("taskSpec volumes lost:\n%s", joinFiles(files))
	}
}

const commandPipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: ci}
spec:
  tasks:
  - name: a
    taskSpec:
      steps:
      - {name: s, image: gcr.io/distroless/static, command: [/app/run], args: [--x, $(params.y)]}
`

func TestCommandStepStaysCommandOnTektonRoundTrip(t *testing.T) {
	files, _ := identityOf(t, map[string]string{"pipeline.yaml": commandPipeline})
	src := joinFiles(files)
	if !strings.Contains(src, "command:") || strings.Contains(src, "script:") {
		t.Fatalf("command step became a script:\n%s", src)
	}
}

func TestOutputValueReachesShellThroughEnv(t *testing.T) {
	j := scriptJob("a", "true")
	j.Outputs = []ir.Output{{Name: "v", Value: "${{ branch }}-${{ inputs.x }}-$(date)"}}
	p := project(j)
	p.Pipelines[0].Inputs = []ir.Input{{Name: "x", Default: "it's $(whoami)"}}
	files, rep := emit(t, p)
	var step doc
	for _, s := range list(get(taskDoc(t, files, "ci-a"), "spec", "steps")) {
		if s.(doc)["name"] == "pipemaster-outputs" {
			step = s.(doc)
		}
	}
	script := step["script"].(string)
	if strings.Contains(script, "$(params") || strings.Contains(script, "'${") || !strings.Contains(script, `"${PM_OUT_0}"`) {
		t.Fatalf("script = %q", script)
	}
	env := named(t, step["env"], "PM_OUT_0")["value"].(string)
	if !strings.Contains(env, "$(params.x)") || !strings.Contains(env, "$(params.ctx-branch)") {
		t.Fatalf("env = %q", env)
	}
	if entry(rep, caps.ScriptInterpolation, "results") != nil {
		t.Fatalf("false lost entry: %v", rep.Entries)
	}
}

func TestAlwaysOnNeedingJobIsReported(t *testing.T) {
	a, b := scriptJob("a", "true"), scriptJob("b", "true")
	b.Needs = []ir.Need{{JobID: "a"}}
	b.If = expr.Call{Name: "always"}
	_, rep := emit(t, project(a, b))
	wantEntry(t, rep, caps.JobIf, caps.Lost, "always()")
}

func TestUnwritableExtIsReported(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Origin = ir.Origin{Provider: "tekton", Dialect: "tekton"}
	p.Pipelines[0].Ext = ir.Ext{"tekton": map[string]any{"description": make(chan int)}}
	_, rep := emit(t, p)
	wantEntry(t, rep, caps.ExtPassthrough, caps.Lost, "description")
}

func TestRequiredInputWithoutDefaultIsReportedOnTrigger(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Inputs = []ir.Input{{Name: "ver", Required: true}}
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	_, rep := emit(t, p)
	wantEntry(t, rep, caps.TriggerPush, caps.External, "required input ver")
}

// A hardened emitted listener keeps its edits: the marker is ignored and the file content is restored.
func TestEditedGeneratedTriggerKeepsEdits(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	files, _ := emit(t, p)
	src := files[".tekton/triggers/eventlistener.yaml"]
	edited := strings.Replace(src, "secretName: pipemaster-github-webhook", "secretName: my-secret", 1)
	edited = strings.Replace(edited, "\n      bindings:\n        - ref: ci-github", "\n        - ref: {name: slack}\n      bindings:\n        - ref: ci-github\n        - ref: extra-binding", 1)
	if edited == src || !strings.Contains(edited, "extra-binding") {
		t.Fatalf("edit did not apply:\n%s", src)
	}
	in := map[string]string{}
	for n, b := range files {
		in[n] = b
	}
	in[".tekton/triggers/eventlistener.yaml"] = edited
	p2, prep, err := tekton.New().Parse(repo(in))
	if err != nil {
		t.Fatal(err)
	}
	if entry(prep, caps.ExprOpaque, "marker ignored") == nil {
		t.Fatalf("no marker-ignored entry: %v", prep.Entries)
	}
	out, _ := emit(t, p2)
	got := out[".tekton/triggers/eventlistener.yaml"]
	for _, want := range []string{"secretName: my-secret", "name: slack", "extra-binding"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q lost:\n%s", want, got)
		}
	}
	if strings.Contains(got, "pipemaster-github-webhook") {
		t.Fatalf("custom secretRef replaced:\n%s", got)
	}
}

const orderedListener = `apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata: {name: gh}
spec:
  triggers:
  - name: on-push
    interceptors:
    - ref: {name: github}
      params:
      - {name: secretRef, value: {secretName: s, secretKey: k}}
      - {name: eventTypes, value: [push]}
      - {name: addChangedFiles, value: {enabled: true}}
    - ref: {name: cel}
      params:
      - {name: filter, value: "extensions.changed_files.matches('a')"}
    - ref: {name: slack}
    template: {ref: run-ci}
---
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerTemplate
metadata: {name: run-ci}
spec:
  resourcetemplates:
  - apiVersion: tekton.dev/v1
    kind: PipelineRun
    spec:
      pipelineRef: {name: ci}
`

func TestSourceInterceptorOrderIsKept(t *testing.T) {
	files, _ := identityOf(t, map[string]string{"pipeline.yaml": triggerFiles, "triggers.yaml": orderedListener})
	var names []string
	for _, d := range docs(t, files[".tekton/triggers/eventlistener.yaml"]) {
		for _, tr := range list(get(d, "spec", "triggers")) {
			for _, ic := range list(get(tr, "interceptors")) {
				names = append(names, get(ic, "ref", "name").(string))
			}
		}
	}
	if strings.Join(names, ",") != "github,cel,slack" {
		t.Fatalf("order = %v", names)
	}
}

func TestScheduleEntryWarnsAboutOpenListener(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule, Cron: "0 1 * * *"}}
	_, rep := emit(t, p)
	wantEntry(t, rep, caps.TriggerSchedule, caps.Emulated, "NetworkPolicy")
}

func TestKeptResourcesAreNotReportedLost(t *testing.T) {
	_, rep := identityOf(t, map[string]string{"pipeline.yaml": triggerFiles, "triggers.yaml": listener})
	if e := entry(rep, caps.ExtPassthrough, "Ext key resources"); e != nil {
		t.Fatalf("false entry: %v", e)
	}
}

func scheduleProject() *ir.Project {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerSchedule, Cron: "0 1 * * *"}}
	return p
}

func TestUneditedScheduleReparsesCleanAndReemitsIdentically(t *testing.T) {
	files, _ := emit(t, scheduleProject())
	p2, prep := reparse(t, files)
	if e := entry(prep, caps.ExprOpaque, "marker ignored"); e != nil {
		t.Fatalf("marker ignored on an unedited file: %v", e)
	}
	again, _ := emit(t, p2)
	if joinFiles(again) != joinFiles(files) {
		t.Fatalf("re-emit differs:\n%s\n---\n%s", joinFiles(files), joinFiles(again))
	}
}

func TestEditedScheduleEntryKeepsEdits(t *testing.T) {
	files, _ := emit(t, scheduleProject())
	const el = ".tekton/triggers/eventlistener.yaml"
	edited := strings.Replace(files[el], "    - name: ci-schedule-0\n      interceptors:\n", "    - name: ci-schedule-0\n      interceptors:\n        - ref: {name: slack}\n", 1)
	if edited == files[el] {
		t.Fatalf("edit did not apply:\n%s", files[el])
	}
	in := map[string]string{}
	for n, b := range files {
		in[n] = b
	}
	in[el] = edited
	p2, prep, err := tekton.New().Parse(repo(in))
	if err != nil {
		t.Fatal(err)
	}
	if entry(prep, caps.ExprOpaque, "marker ignored") == nil {
		t.Fatalf("no marker-ignored entry: %v", prep.Entries)
	}
	out, _ := emit(t, p2)
	got := out[el]
	if strings.Count(got, "name: slack") != 1 || strings.Count(got, "name: ci-schedule-0") != 1 {
		t.Fatalf("edit lost or entry written twice:\n%s", got)
	}
}

func TestRequiredInputKeepsItsDefault(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Inputs = []ir.Input{{Name: "env", Required: true, Default: "staging"}}
	files, rep := emit(t, p)
	params := list(get(pipelineDoc(t, files, "ci"), "spec", "params"))
	if len(params) != 1 || get(params[0], "default") != "staging" {
		t.Fatalf("default dropped: %v", params)
	}
	wantEntry(t, rep, caps.PipelineInputs, caps.Lost, "required")
}

func TestAlwaysOnStepIsReported(t *testing.T) {
	j := scriptJob("a", "true")
	j.Steps = append(j.Steps, &ir.Step{ID: "cleanup", Kind: ir.StepScript, Script: []string{"echo done"}, If: expr.Call{Name: "always"}})
	_, rep := emit(t, project(j))
	wantEntry(t, rep, caps.StepIf, caps.Lost, "always()")
}

func TestJobContainerEntrypointIsReported(t *testing.T) {
	j := scriptJob("a", "true")
	j.Container = &ir.Image{Ref: "golang:1.22", Entrypoint: []string{""}}
	_, rep := emit(t, project(j))
	wantEntry(t, rep, caps.JobContainer, caps.Lost, "entrypoint")
}

func renamedListener(t *testing.T, from, to string) (*ir.Project, *report.Report, map[string]string) {
	t.Helper()
	files, _ := emit(t, scheduleProject())
	const el = ".tekton/triggers/eventlistener.yaml"
	files[el] = strings.Replace(files[el], "    - name: "+from+"\n", "    - name: "+to+"\n", 1)
	if !strings.Contains(files[el], "name: "+to) {
		t.Fatalf("rename did not apply:\n%s", files[el])
	}
	back, rep := reparse(t, files)
	return back, rep, files
}

func TestRenamedScheduleEntryIsKept(t *testing.T) {
	back, rep, _ := renamedListener(t, "ci-schedule-0", "mine")
	if !hasEntry(rep, "marker ignored") {
		t.Fatalf("no marker ignored entry: %v", rep.Entries)
	}
	out, _ := emit(t, back)
	if !strings.Contains(joinFiles(out), "name: mine") {
		t.Fatalf("renamed entry lost:\n%s", joinFiles(out))
	}
}

func TestRenamedPushEntryReportsMarkerIgnored(t *testing.T) {
	back, rep, _ := renamedListener(t, "ci-push-0", "mine")
	if !hasEntry(rep, "marker ignored") {
		t.Fatalf("no marker ignored entry: %v", rep.Entries)
	}
	out, _ := emit(t, back)
	if !strings.Contains(joinFiles(out), "name: mine") {
		t.Fatalf("renamed entry lost:\n%s", joinFiles(out))
	}
}

func TestKeptListenerEntryIsNotReportedLost(t *testing.T) {
	back, _, _ := renamedListener(t, "ci-schedule-0", "mine")
	out, rep := emit(t, back)
	if !strings.Contains(joinFiles(out), "name: mine") {
		t.Fatalf("kept entry not written:\n%s", joinFiles(out))
	}
	if e := entry(rep, caps.TriggerAPI, ""); e != nil && e.Level == caps.Lost {
		t.Fatalf("false lost entry: %v", e)
	}
}

func TestForeignAPITriggerIsReportedLost(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerAPI}}
	_, rep := emit(t, p)
	wantEntry(t, rep, caps.TriggerAPI, caps.Lost, "API trigger")
}

func TestRemoteTaskKeepsItsPipelineTaskKeys(t *testing.T) {
	src := `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: p
spec:
  workspaces:
    - name: ws
  tasks:
    - name: first
      taskRef:
        name: remote
      displayName: First
      timeout: 1h0m0s
      retries: 2
      onError: continue
      when:
        - input: $(params.x)
          operator: in
          values: [a]
      workspaces:
        - name: source
          workspace: ws
  params:
    - name: x
      default: a
`
	p, _ := parseFiles(t, map[string]string{"p.yaml": src})
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("PipelineTask keys changed:\n%s\n%s", diff, joinFiles(files))
	}
}

func TestPipelineRunSpecKeysSurviveEmit(t *testing.T) {
	src := `apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  generateName: ci-
spec:
  taskRunTemplate:
    serviceAccountName: default
  params:
    - name: repo
      value: "{{repo}}"
  pipelineSpec:
    tasks:
      - name: a
        taskSpec:
          steps: [{name: s, image: x, script: "true"}]
`
	p, _ := parseFiles(t, map[string]string{"r.yaml": src})
	files, rep := emit(t, p)
	back, _ := reparse(t, files)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("PipelineRun keys changed:\n%s\n%s", diff, joinFiles(files))
	}
	if e := entry(rep, caps.ExtPassthrough, "pipelineRun"); e != nil && e.Level == caps.Lost {
		t.Fatalf("keys reported lost but written: %v", e)
	}
}

func TestOwnCELWhenEntryIsWrittenBack(t *testing.T) {
	src := `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: p
spec:
  params:
    - name: run
      default: "true"
  tasks:
    - name: a
      when:
        - cel: "'$(params.run)' == 'true'"
      taskSpec:
        steps: [{name: s, image: x, script: "true"}]
`
	p, _ := parseFiles(t, map[string]string{"p.yaml": src})
	files, rep := emit(t, p)
	if !strings.Contains(joinFiles(files), "cel:") {
		t.Fatalf("cel entry dropped:\n%s", joinFiles(files))
	}
	wantEntry(t, rep, caps.ExprOpaque, caps.External, "enable-cel-in-whenexpression")
	back, _ := reparse(t, files)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("cel entry changed:\n%s", diff)
	}
}

func TestRelativeWorkDirReturnsAsWritten(t *testing.T) {
	for _, wd := range []string{"tokio", "./docs", "./${{ matrix.module }}"} {
		j := &ir.Job{ID: "a", Steps: []*ir.Step{
			{ID: "co", Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "actions", Repo: "checkout", Ref: "v4"}},
			{ID: "run", Kind: ir.StepScript, Script: []string{"ls"}, WorkDir: wd},
		}}
		p := project(j)
		p.Pipelines[0].Jobs[0].Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "module", Values: []string{"a"}}}}
		files, _ := emit(t, p)
		back, _ := reparse(t, files)
		if got := back.Pipelines[0].Jobs[0].Steps[len(back.Pipelines[0].Jobs[0].Steps)-1].WorkDir; got != wd {
			t.Fatalf("work dir %q came back as %q", wd, got)
		}
	}
}

func TestOptionalNeedIsReportedLost(t *testing.T) {
	a, b := scriptJob("a", "true"), scriptJob("b", "true")
	b.Needs = []ir.Need{{JobID: "a", Optional: true}}
	_, rep := emit(t, project(a, b))
	wantEntry(t, rep, caps.JobNeeds, caps.Lost, "optional")
}

func TestJobGuardBesideStepConditionsReturns(t *testing.T) {
	j := scriptJob("a", "make")
	j.If = expr.Binary{Op: expr.OpOr, L: eqStr([]string{"branch"}, "main"), R: eqStr([]string{"event"}, "schedule")}
	j.Steps = append(j.Steps, &ir.Step{ID: "tail", Kind: ir.StepScript, Script: []string{"done"}, If: expr.Unary{Op: expr.OpNot, X: eqStr([]string{"event"}, "pull_request")}})
	p := project(j)
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	got, want := back.Pipelines[0].Jobs[0], j
	if cond(got.If) != cond(want.If) || got.Steps[0].If != nil || cond(got.Steps[1].If) != cond(want.Steps[1].If) {
		t.Fatalf("conditions changed: job %q, steps %q %q\n%s", cond(got.If), cond(got.Steps[0].If), cond(got.Steps[1].If), joinFiles(files))
	}
}

func TestJobGuardOnAPlaceholderStepReturns(t *testing.T) {
	j := &ir.Job{ID: "a", Steps: []*ir.Step{{ID: "p", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/docker"}, With: map[string]ir.Value{"repo": {Lit: "x"}}}}}
	j.If = expr.Binary{Op: expr.OpOr, L: eqStr([]string{"branch"}, "main"), R: eqStr([]string{"event"}, "schedule")}
	files, _ := emit(t, project(j))
	back, _ := reparse(t, files)
	got := back.Pipelines[0].Jobs[0]
	if cond(got.If) != cond(j.If) || len(got.Steps) != 1 || got.Steps[0].ID != "p" {
		t.Fatalf("job condition = %q, steps = %d\n%s", cond(got.If), len(got.Steps), joinFiles(files))
	}
}

func TestStepConditionOnANeedsOutputReturns(t *testing.T) {
	a := scriptJob("a", "true")
	a.Outputs = []ir.Output{{Name: "dry", Value: "x"}}
	b := scriptJob("b", "true")
	b.Needs = []ir.Need{{JobID: "a"}}
	b.Steps[0].If = expr.Binary{Op: expr.OpNe, L: expr.Ref{Path: []string{"needs", "a", "outputs", "dry"}}, R: expr.Lit{Kind: expr.LitStr, Str: "true"}}
	files, _ := emit(t, project(a, b))
	back, _ := reparse(t, files)
	if got, want := cond(back.Pipelines[0].Jobs[1].Steps[0].If), cond(b.Steps[0].If); got != want {
		t.Fatalf("step condition = %q, want %q\n%s", got, want, joinFiles(files))
	}
}

func TestStepSecurityContextSurvivesTektonIdentity(t *testing.T) {
	st := &ir.Step{ID: "run", Kind: ir.StepScript, Script: []string{"id"}, Image: &ir.Image{Ref: "alpine:3"},
		Ext: ir.Ext{"tekton": map[string]any{"securityContext": map[string]any{"runAsUser": 0}}}}
	p := project(&ir.Job{ID: "a", Steps: []*ir.Step{st}})
	p.Origin = ir.Origin{Provider: "tekton", Dialect: "tekton"}
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	got, _ := back.Pipelines[0].Jobs[0].Steps[0].Ext["tekton"].(map[string]any)
	if got["securityContext"] == nil {
		t.Fatalf("securityContext lost\n%s", joinFiles(files))
	}
}

func TestFinallyNeedFromAResultIsNotDuplicated(t *testing.T) {
	a := scriptJob("a", "true")
	a.Outputs = []ir.Output{{Name: "sha", Value: "x"}}
	done := scriptJob("done", "echo ${{ needs.a.outputs.sha }}")
	done.Needs = []ir.Need{{JobID: "a"}}
	p := project(a)
	p.Pipelines[0].Finally = []*ir.Job{done}
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	if got := back.Pipelines[0].Finally[0].Needs; len(got) != 1 {
		t.Fatalf("needs = %v\n%s", got, joinFiles(files))
	}
}

func TestResultReferenceInARemoteTaskKeepsItsImpliedNeed(t *testing.T) {
	identityOf(t, map[string]string{"p.yaml": `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: p
spec:
  tasks:
    - name: first
      taskSpec:
        results:
          - name: sha
        steps:
          - name: s
            image: alpine
            script: "true"
    - name: second
      taskRef:
        resolver: git
        params:
          - name: repo
            value: r
      params:
        - name: COMMIT
          value: $(tasks.first.results.sha)
`})
}

func TestCommandFormSurvivesASplitScript(t *testing.T) {
	st := &ir.Step{ID: "run", Kind: ir.StepScript, Image: &ir.Image{Ref: "alpine:3"},
		Script: []string{"/bin/sh -c 'echo a", "echo b'"},
		Ext: ir.Ext{"tekton": map[string]any{
			"command": []any{"/bin/sh", "-c"}, "args": []any{"echo a\necho b"}, "script_line": "/bin/sh -c 'echo a\necho b'"}}}
	p := project(&ir.Job{ID: "a", Steps: []*ir.Step{st}})
	p.Origin = ir.Origin{Provider: "tekton", Dialect: "tekton"}
	files, _ := emit(t, p)
	if src := joinFiles(files); !strings.Contains(src, "command:") {
		t.Fatalf("command form lost\n%s", src)
	}
}

func TestJobWithNoStepReportsThePlaceholderAsLost(t *testing.T) {
	_, rep := emit(t, project(&ir.Job{ID: "a"}))
	var found bool
	for _, e := range rep.Entries {
		found = found || (e.Feature == caps.StepScript && e.Level == caps.Lost && e.IRPath == "pipeline/ci/job/a/step/script")
	}
	if !found {
		t.Fatalf("no lost placeholder entry: %v", rep.Entries)
	}
}

func TestCommentedScriptLineBindsNoEnv(t *testing.T) {
	st := &ir.Step{ID: "run", Kind: ir.StepScript, Image: &ir.Image{Ref: "alpine:3"},
		Script: []string{"echo ${{ inputs.target }} ${{ vars.LEVEL }}"}}
	files, _ := emit(t, project(&ir.Job{ID: "a", Steps: []*ir.Step{st}}))
	if src := joinFiles(files); strings.Contains(src, "LEVEL") && strings.Contains(src, "valueFrom") {
		t.Fatalf("a commented line left its env binding\n%s", src)
	}
}

func TestJobGuardOnAStepWithNoScriptLineReturns(t *testing.T) {
	j := scriptJob("a", "make")
	j.If = expr.Binary{Op: expr.OpOr, L: eqStr([]string{"branch"}, "main"), R: eqStr([]string{"event"}, "schedule")}
	j.Steps = append([]*ir.Step{{ID: "empty", Kind: ir.StepScript}}, j.Steps...)
	files, _ := emit(t, project(j))
	back, _ := reparse(t, files)
	got := back.Pipelines[0].Jobs[0]
	if cond(got.If) != cond(j.If) || got.Steps[0].If != nil || got.Steps[1].If != nil {
		t.Fatalf("job %q, steps %q %q\n%s", cond(got.If), cond(got.Steps[0].If), cond(got.Steps[1].If), joinFiles(files))
	}
}

func TestInputTypeWithoutTektonFormIsWrittenAsString(t *testing.T) {
	for _, typ := range []string{"choice", "boolean", "number"} {
		t.Run(typ, func(t *testing.T) {
			p := project(scriptJob("a", "true"))
			p.Pipelines[0].Inputs = []ir.Input{{Name: "mode", Type: typ, Default: "x"}}
			files, rep := emit(t, p)
			params := list(get(pipelineDoc(t, files, "ci"), "spec", "params"))
			if len(params) != 1 || get(params[0], "type") != "string" {
				t.Fatalf("type = %v", params)
			}
			wantEntry(t, rep, caps.PipelineInputs, caps.Lost, typ)
		})
	}
}

func TestTektonParamTypesAreKept(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Inputs = []ir.Input{{Name: "l", Type: "array"}, {Name: "o", Type: "object"}, {Name: "s", Type: "string"}}
	files, rep := emit(t, p)
	params := list(get(pipelineDoc(t, files, "ci"), "spec", "params"))
	for i, want := range []string{"array", "object", "string"} {
		if get(params[i], "type") != want {
			t.Fatalf("param %d: %v", i, params[i])
		}
	}
	if hasEntry(rep, "has no Tekton param type") {
		t.Fatalf("native type reported: %+v", rep.Entries)
	}
}

func TestFinallyTaskReadingAResultKeepsOneNeed(t *testing.T) {
	main := scriptJob("main", "true")
	main.Outputs = []ir.Output{{Name: "o"}}
	f := scriptJob("f", "echo ${{ needs.main.outputs.o }}")
	f.Needs = []ir.Need{{JobID: "main"}}
	p := project(main)
	p.Pipelines[0].Finally = []*ir.Job{f}
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	if n := back.Pipelines[0].Finally[0].Needs; len(n) != 1 || n[0].JobID != "main" {
		t.Fatalf("needs = %+v\n%s", n, joinFiles(files))
	}
}
