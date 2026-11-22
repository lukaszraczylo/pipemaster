package argo_test

import (
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/argo"
)

func TestDetect(t *testing.T) {
	good := dagOf("      - {name: a, template: echo}\n", echoTemplates)
	cases := map[string]struct {
		files map[string]string
		want  bool
	}{
		"workflow template":  {map[string]string{".argo/a.yaml": good}, true},
		"cluster template":   {map[string]string{".argo/a.yml": strings.Replace(good, "kind: WorkflowTemplate", "kind: ClusterWorkflowTemplate", 1)}, true},
		"workflow":           {map[string]string{".argo/w/a.yaml": strings.Replace(good, "kind: WorkflowTemplate", "kind: Workflow", 1)}, true},
		"cron workflow":      {map[string]string{".argo/c.yaml": "apiVersion: argoproj.io/v1alpha1\nkind: CronWorkflow\nmetadata: {name: n}\nspec: {schedules: ['* * * * *'], workflowSpec: {workflowTemplateRef: {name: ci}}}\n"}, true},
		"eventbus only":      {map[string]string{".argo/bus.yaml": "apiVersion: argoproj.io/v1alpha1\nkind: EventBus\nmetadata: {name: default}\nspec: {nats: {native: {}}}\n"}, false},
		"sensor only":        {map[string]string{".argo/s.yaml": "apiVersion: argoproj.io/v1alpha1\nkind: Sensor\nmetadata: {name: s}\nspec: {}\n"}, false},
		"other api version":  {map[string]string{".argo/a.yaml": strings.Replace(good, "argoproj.io/v1alpha1", "example.com/v1", 1)}, false},
		"outside .argo":      {map[string]string{"a.yaml": good}, false},
		"helm template only": {map[string]string{".argo/a.yaml": "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: {{ .Values.name }}}\n"}, false},
		"not yaml":           {map[string]string{".argo/a.yaml": "a: [\n"}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := argo.New().Detect(repo(c.files)); got != c.want {
				t.Fatalf("Detect = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHelmFilesAreSkippedWithAnEntry(t *testing.T) {
	good := dagOf("      - {name: a, template: echo}\n", echoTemplates)
	helm := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata:\n  name: {{ .Values.name }}\n{{- if .Values.x }}\nspec: {}\n{{- end }}\n"
	p, rep := parseFiles(t, map[string]string{"good.yaml": good, "chart/wf.yaml": helm})
	if len(p.Pipelines) != 1 {
		t.Fatalf("pipelines = %d, want 1", len(p.Pipelines))
	}
	if !hasEntry(rep, ".argo/chart/wf.yaml") || !hasEntry(rep, "Helm") {
		t.Fatalf("no entry names the Helm file: %+v", rep.Entries)
	}
	// Argo's own tags are not Helm text.
	if hasEntry(rep, ".argo/good.yaml") {
		t.Fatalf("a file with Argo tags was skipped: %+v", rep.Entries)
	}
	if _, _, err := argo.New().Parse(repo(map[string]string{".argo/wf.yaml": helm})); err == nil {
		t.Fatal("a repo with only Helm files parsed")
	}
}

func TestPipelineIDs(t *testing.T) {
	body := "spec:\n  entrypoint: main\n  templates:\n  - {name: main, container: {image: x}}\n"
	cases := map[string]struct{ head, id string }{
		"name":         {"kind: WorkflowTemplate\nmetadata: {name: ci}\n", "ci"},
		"generateName": {"kind: Workflow\nmetadata: {generateName: build-run-}\n", "build-run"},
		"cluster":      {"kind: ClusterWorkflowTemplate\nmetadata: {name: shared}\n", "shared"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pl, _ := parseYAML(t, "apiVersion: argoproj.io/v1alpha1\n"+c.head+body)
			if pl.ID != c.id {
				t.Fatalf("ID = %q, want %q", pl.ID, c.id)
			}
		})
	}
}

func TestMultiDocumentFile(t *testing.T) {
	a := dagOf("      - {name: a, template: echo}\n", echoTemplates)
	b := strings.Replace(a, "name: ci}", "name: other}", 1)
	p, _ := parseFiles(t, map[string]string{"both.yaml": a + "---\n" + b})
	if len(p.Pipelines) != 2 || p.Pipelines[0].ID != "ci" || p.Pipelines[1].ID != "other" {
		t.Fatalf("pipelines = %v", p.Pipelines)
	}
}

func TestArgumentsMapToInputs(t *testing.T) {
	pl, rep := parseYAML(t, `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci}
spec:
  entrypoint: main
  arguments:
    parameters:
    - {name: version, value: "1.2", description: Release version}
    - {name: target}
    - {name: env, value: dev, enum: [dev, prod]}
    - {name: ctx-branch, value: main}
  templates:
  - {name: main, container: {image: x}}
`)
	want := []ir.Input{
		{Name: "version", Default: "1.2", Description: "Release version"},
		{Name: "target", Required: true},
		{Name: "env", Default: "dev"},
	}
	if !reflect.DeepEqual(pl.Inputs, want) {
		t.Fatalf("Inputs = %+v, want %+v", pl.Inputs, want)
	}
	params, _ := ext(pl.Ext)["params"].(map[string]any)
	if params["env"] == nil || !hasEntry(rep, "param env: key enum") {
		t.Fatalf("enum not kept: %v / %+v", params, rep.Entries)
	}
}

func TestExitHandlerBecomesFinally(t *testing.T) {
	pl, _ := parseYAML(t, `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci}
spec:
  entrypoint: main
  hooks:
    exit: {template: exit}
  templates:
  - name: main
    dag: {tasks: [{name: build, template: echo}]}
  - name: exit
    dag:
      tasks:
      - {name: notify-fail, template: echo, when: "{{workflow.status}} == Failed"}
      - {name: notify-ok, template: echo, when: "{{= workflow.status == 'Succeeded' }}"}
      - {name: always, template: echo}
  - name: echo
    container: {image: x}
`)
	if !reflect.DeepEqual(ids(pl.Jobs), []string{"build"}) || !reflect.DeepEqual(ids(pl.Finally), []string{"always", "notify-fail", "notify-ok"}) {
		t.Fatalf("jobs %v finally %v", ids(pl.Jobs), ids(pl.Finally))
	}
	if got := cond(job(t, pl, "notify-fail").If); got != "failure()" {
		t.Fatalf("notify-fail If = %q", got)
	}
	if got := cond(job(t, pl, "notify-ok").If); got != "success()" {
		t.Fatalf("notify-ok If = %q", got)
	}
	if job(t, pl, "always").If != nil {
		t.Fatal("always has a condition")
	}
}

func TestDeprecatedOnExit(t *testing.T) {
	pl, _ := parseYAML(t, `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci}
spec:
  entrypoint: main
  onExit: cleanup
  templates:
  - {name: main, container: {image: x}}
  - {name: cleanup, container: {image: x}}
`)
	if !reflect.DeepEqual(ids(pl.Finally), []string{"cleanup"}) {
		t.Fatalf("finally %v", ids(pl.Finally))
	}
}

func TestSynchronizationMutexIsConcurrency(t *testing.T) {
	pl, rep := parseYAML(t, `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci}
spec:
  entrypoint: main
  synchronization:
    mutexes: [{name: deploy-lock}]
    semaphores: [{configMapKeyRef: {name: cfg, key: n}}]
  templates:
  - {name: main, container: {image: x}}
`)
	if pl.Concurrency == nil || pl.Concurrency.Group != "deploy-lock" || pl.Concurrency.CancelInProgress {
		t.Fatalf("Concurrency = %+v", pl.Concurrency)
	}
	sync, _ := ext(pl.Ext)["synchronization"].(map[string]any)
	if sync["semaphores"] == nil || !hasEntry(rep, "semaphores") {
		t.Fatalf("semaphores lost: %v", ext(pl.Ext))
	}
}

func TestMetadataAndSpecKeysAreKept(t *testing.T) {
	pl, rep := parseYAML(t, `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci, namespace: ci-ns, labels: {team: a}}
spec:
  entrypoint: main
  serviceAccountName: runner
  ttlStrategy: {secondsAfterCompletion: 60}
  templates:
  - {name: main, container: {image: x}}
`)
	e := ext(pl.Ext)
	if e["serviceAccountName"] != "runner" || e["ttlStrategy"] == nil || e["metadata"] == nil {
		t.Fatalf("Ext = %v", e)
	}
	if !hasEntry(rep, "key serviceAccountName has no IR field") || !hasEntry(rep, "metadata has no IR field") {
		t.Fatalf("entries %+v", rep.Entries)
	}
}

func TestDuplicateWorkflowTemplateKeepsTheLaterOne(t *testing.T) {
	a := dagOf("      - {name: a, template: echo}\n", echoTemplates)
	b := strings.Replace(a, "name: a,", "name: z,", 1)
	p, rep := parseFiles(t, map[string]string{"a.yaml": a, "b.yaml": b})
	if len(p.Pipelines) != 1 || job(t, p.Pipelines[0], "a") == nil {
		t.Fatalf("first template not used: %v", p.Pipelines)
	}
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	if len(res) != 1 || !strings.Contains(fmtAny(res[0]), "z") || !hasEntry(rep, "WorkflowTemplate ci is defined twice") {
		t.Fatalf("later document lost: %v %+v", res, rep.Entries)
	}
}

func TestUnreachedTemplatesAreKept(t *testing.T) {
	pl, rep := parseYAML(t, dagOf("      - {name: a, template: echo}\n", echoTemplates+"  - {name: spare, container: {image: y}}\n"))
	list, _ := ext(pl.Ext)["templates"].([]any)
	if len(list) != 1 || !strings.Contains(fmtAny(list[0]), "spare") || !hasEntry(rep, "template spare is not reached") {
		t.Fatalf("templates = %v %+v", list, rep.Entries)
	}
}

func TestWorkflowThatOnlyRefsATemplateIsAManualRun(t *testing.T) {
	tpl := dagOf("      - {name: a, template: echo}\n", echoTemplates)
	run := "apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nmetadata: {generateName: ci-run-, labels: {x: y}}\nspec:\n  workflowTemplateRef: {name: ci}\n  arguments: {parameters: [{name: version, value: '3'}]}\n"
	p, rep := parseFiles(t, map[string]string{"t.yaml": tpl, "run.yaml": run})
	if len(p.Pipelines) != 1 {
		t.Fatalf("a run became a pipeline: %v", ids2(p))
	}
	trs := p.Pipelines[0].Triggers
	if len(trs) != 1 || trs[0].Kind != ir.TriggerManual {
		t.Fatalf("triggers = %+v", trs)
	}
	wf, _ := ext(trs[0].Ext)["workflow"].(map[string]any)
	if wf == nil || !strings.Contains(fmtAny(wf), "version") || !hasEntry(rep, "Workflow") {
		t.Fatalf("run data lost: %v %+v", ext(trs[0].Ext), rep.Entries)
	}
}

func TestRunOfAnUnknownTemplateIsKept(t *testing.T) {
	tpl := dagOf("      - {name: a, template: echo}\n", echoTemplates)
	run := "apiVersion: argoproj.io/v1alpha1\nkind: Workflow\nmetadata: {name: r}\nspec: {workflowTemplateRef: {name: nope}}\n"
	p, rep := parseFiles(t, map[string]string{"t.yaml": tpl, "run.yaml": run})
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	if len(res) != 1 || !hasEntry(rep, "kind: Workflow has no IR form") {
		t.Fatalf("resources = %v %+v", res, rep.Entries)
	}
}

func ids2(p *ir.Project) []string {
	var out []string
	for _, pl := range p.Pipelines {
		out = append(out, pl.ID)
	}
	return out
}

func TestSourceFilesAndSanitizeID(t *testing.T) {
	a := argo.New()
	lay, ok := a.(provider.Layout)
	if !ok {
		t.Fatal("no Layout")
	}
	fsys := repo(map[string]string{".argo/b.yaml": "x: 1", ".argo/a/c.yml": "x: 1", ".argo/readme.md": "x", "other/z.yaml": "x"})
	files, err := lay.SourceFiles(fsys)
	if err != nil || !reflect.DeepEqual(files, []string{".argo/a/c.yml", ".argo/b.yaml"}) {
		t.Fatalf("SourceFiles = %v, %v", files, err)
	}
	var _ fs.FS = fsys
	rules, ok := a.(provider.IDRules)
	if !ok {
		t.Fatal("no IDRules")
	}
	long := strings.Repeat("a", 70)
	for in, want := range map[string]string{"build": "build", "Build_Go": "build-go", "": "default", "--x--": "x"} {
		if got := rules.SanitizeID(in); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
	got := rules.SanitizeID(long)
	if len(got) > 63 || got == rules.SanitizeID(long+"b") {
		t.Errorf("long id %q", got)
	}
}

// A library WorkflowTemplate whose templates are all called still has its other keys; they stay in Ext.
func TestLibraryKeysSurviveWhenEveryTemplateIsCalled(t *testing.T) {
	lib := "apiVersion: argoproj.io/v1alpha1\nkind: WorkflowTemplate\nmetadata: {name: lib, labels: {team: a}}\nspec:\n  serviceAccountName: lib-sa\n  arguments: {parameters: [{name: p}]}\n  templates:\n  - {name: lint, container: {image: l, command: [lint]}}\n"
	main := dagOf("      - {name: a, templateRef: {name: lib, template: lint}}\n", "")
	p, rep := parseFiles(t, map[string]string{"lib.yaml": lib, "ci.yaml": main})
	res, _ := ext(p.Pipelines[0].Ext)["resources"].([]any)
	if len(res) != 1 || !strings.Contains(fmt.Sprint(res), "lib-sa") || !hasEntry(rep, "lib") {
		t.Fatalf("library keys lost: %v %+v", res, rep.Entries)
	}
}
