package tekton_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

const localGitClone = `apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: git-clone
spec:
  steps:
  - name: local
    image: alpine
    script: echo local
`

func catalogPipeline(task string) string {
	return pipelineWith("  tasks:\n  - name: t\n" + task)
}

func TestCatalogLookup(t *testing.T) {
	cluster := func(name string) string {
		return "    taskRef:\n      resolver: cluster\n      params:\n      - {name: name, value: " + name + "}\n      - {name: kind, value: task}\n"
	}
	tests := []struct {
		name     string
		task     string
		local    string
		wantStep ir.StepKind
		wantImg  string
		mapped   bool
	}{
		{name: "git-clone by name", task: "    taskRef: {name: git-clone}\n", wantStep: ir.StepAction, mapped: true},
		{name: "ClusterTask kind", task: "    taskRef: {name: git-clone, kind: ClusterTask}\n", wantStep: ir.StepAction, mapped: true},
		{name: "git-clone cluster resolver", task: cluster("git-clone"), wantStep: ir.StepAction, mapped: true},
		{name: "buildah cluster resolver", task: cluster("buildah"), wantStep: ir.StepScript, wantImg: "quay.io/buildah/stable:v1", mapped: true},
		{name: "kaniko by name", task: "    taskRef: {name: kaniko}\n", wantStep: ir.StepScript, wantImg: "gcr.io/kaniko-project/executor:v1.14.0-debug", mapped: true},
		{name: "golang-test by name", task: "    taskRef: {name: golang-test}\n", wantStep: ir.StepScript, wantImg: "docker.io/library/golang:latest", mapped: true},
		{name: "golang-build by name", task: "    taskRef: {name: golang-build}\n", wantStep: ir.StepScript, wantImg: "docker.io/library/golang:latest", mapped: true},
		{name: "yaml-lint by name", task: "    taskRef: {name: yaml-lint}\n", wantStep: ir.StepScript, mapped: true},
		{
			name: "hub resolver", wantStep: ir.StepScript, wantImg: "quay.io/buildah/stable:v1", mapped: true,
			task: "    taskRef:\n      resolver: hub\n      params:\n      - {name: name, value: buildah}\n      - {name: version, value: \"0.9\"}\n",
		},
		{
			name: "bundles resolver", wantStep: ir.StepScript, wantImg: "quay.io/buildah/stable:v1", mapped: true,
			task: "    taskRef:\n      resolver: bundles\n      params:\n      - {name: bundle, value: ghcr.io/x/catalog}\n      - {name: name, value: buildah}\n",
		},
		{name: "legacy bundle field", task: "    taskRef: {name: buildah, bundle: ghcr.io/x/catalog}\n", wantStep: ir.StepScript, wantImg: "quay.io/buildah/stable:v1", mapped: true},
		{
			name: "git resolver path basename", wantStep: ir.StepAction, mapped: true,
			task: "    taskRef:\n      resolver: git\n      params:\n      - {name: pathInRepo, value: task/git-clone/0.10/git-clone.yaml}\n",
		},
		{name: "unknown resolver name", task: cluster("no-such-task"), wantStep: ir.StepTask},
		{name: "unknown plain name", task: "    taskRef: {name: no-such-task}\n", wantStep: ir.StepTask},
		{name: "pipeline kind is no task", task: "    taskRef:\n      resolver: cluster\n      params:\n      - {name: name, value: buildah}\n      - {name: kind, value: pipeline}\n", wantStep: ir.StepTask},
		{name: "local Task wins without resolver", task: "    taskRef: {name: git-clone}\n", local: localGitClone, wantStep: ir.StepScript, wantImg: "alpine"},
		{name: "resolver never reads the local Task", task: cluster("git-clone"), local: localGitClone, wantStep: ir.StepAction, mapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"p.yaml": catalogPipeline(tt.task)}
			if tt.local != "" {
				files["task.yaml"] = tt.local
			}
			p, rep := parseFiles(t, files)
			j := job(t, p.Pipelines[0], "t")
			if len(j.Steps) == 0 {
				t.Fatal("the job has no step")
			}
			last := j.Steps[len(j.Steps)-1]
			if last.Kind != tt.wantStep {
				t.Fatalf("steps = %+v, want last kind %s", j.Steps, tt.wantStep)
			}
			if tt.wantStep == ir.StepScript && tt.mapped {
				first := j.Steps[0]
				if len(j.Steps) != 2 || first.Kind != ir.StepAction || first.Action == nil || first.Action.Repo != "checkout" {
					t.Fatalf("a mapped task that reads the workspace needs its own checkout first: %+v", j.Steps)
				}
			}
			img := j.Container
			if img == nil {
				img = last.Image
			}
			if tt.wantImg != "" && (img == nil || img.Ref != tt.wantImg) {
				t.Fatalf("image = %+v, want %s", img, tt.wantImg)
			}
			_, kept := ext(j.Ext)[tekton.ExtCatalogRef]
			if kept != tt.mapped {
				t.Fatalf("catalog_ref kept = %v, want %v; ext = %v", kept, tt.mapped, j.Ext)
			}
			emulated := false
			for _, e := range rep.Entries {
				emulated = emulated || (e.Level == caps.Emulated && strings.Contains(e.Strategy, "catalog task"))
			}
			if emulated != tt.mapped {
				t.Fatalf("emulated verdict = %v, want %v; entries = %+v", emulated, tt.mapped, rep.Entries)
			}
			if tt.wantStep == ir.StepTask && !hasEntry(rep, "remote") && !hasEntry(rep, "not-in-files") && !hasEntry(rep, "no-such-task") && !hasEntry(rep, "unresolved") {
				t.Fatalf("an unmapped reference needs an entry: %+v", rep.Entries)
			}
		})
	}
}

func TestCatalogCheckoutParams(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"p.yaml": catalogPipeline(`    taskRef: {name: git-clone}
    params:
    - {name: URL, value: https://example.com/r.git}
    - {name: REVISION, value: v1}
    - {name: DEPTH, value: "0"}
    - {name: SUBMODULES, value: "false"}
    - {name: DELETE_EXISTING, value: "true"}
    - {name: SUBDIRECTORY, value: src}
`)})
	st := job(t, p.Pipelines[0], "t").Steps[0]
	if st.Action == nil || st.Action.Owner != "actions" || st.Action.Repo != "checkout" {
		t.Fatalf("action = %+v", st.Action)
	}
	want := map[string]string{"ref": "v1", "fetch-depth": "0", "submodules": "false"}
	for k, v := range want {
		if st.With[k].Lit != v {
			t.Errorf("with[%s] = %+v, want %s", k, st.With[k], v)
		}
	}
	for _, name := range []string{"URL", "SUBDIRECTORY"} {
		if !hasEntry(rep, "param "+name+" of the catalog task git-clone has no checkout input") {
			t.Errorf("unmapped param %s is not reported", name)
		}
	}
	if hasEntry(rep, "param DELETE_EXISTING") {
		t.Error("a param at its catalog default needs no entry")
	}
}

func TestCatalogUnresolvedParamStaysRemote(t *testing.T) {
	p, rep := parseFiles(t, map[string]string{"p.yaml": catalogPipeline(`    taskRef: {name: buildah}
    params:
    - {name: IMAGE, value: "$(tasks.other.results.image)"}
`)})
	j := job(t, p.Pipelines[0], "t")
	if len(j.Steps) != 1 || j.Steps[0].Kind != ir.StepTask {
		t.Fatalf("steps = %+v", j.Steps)
	}
	if !hasEntry(rep, "binds a Tekton variable") {
		t.Fatalf("entries = %+v", rep.Entries)
	}
}

func TestCatalogArrayParamIsJoined(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"p.yaml": catalogPipeline(`    taskRef: {name: yaml-lint}
    params:
    - {name: args, value: ["-d", "relaxed", "."]}
`)})
	steps := job(t, p.Pipelines[0], "t").Steps
	st := steps[len(steps)-1]
	if len(st.Script) == 0 || !strings.Contains(strings.Join(st.Script, "\n"), "yamllint -d relaxed .") {
		t.Fatalf("script = %q", st.Script)
	}
}

func TestCatalogKanikoClearsTheEntrypoint(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"p.yaml": catalogPipeline("    taskRef: {name: kaniko}\n")})
	c := job(t, p.Pipelines[0], "t").Container
	if c == nil || len(c.Entrypoint) != 1 || c.Entrypoint[0] != "" {
		t.Fatalf("container = %+v, want entrypoint [\"\"]", c)
	}
}

func TestCatalogReportsWhatTheFileCannotSet(t *testing.T) {
	for name, want := range map[string][]string{
		"buildah": {"privileged runner", "registry credentials"},
		"kaniko":  {"registry credentials"},
	} {
		_, rep := parseFiles(t, map[string]string{"p.yaml": catalogPipeline("    taskRef: {name: " + name + "}\n")})
		for _, w := range want {
			found := false
			for _, e := range rep.Entries {
				found = found || (e.Level == caps.External && strings.Contains(e.Detail, w) && strings.Contains(e.IRPath, "/job/t"))
			}
			if !found {
				t.Errorf("%s: no external entry for %q: %+v", name, w, rep.Entries)
			}
		}
	}
}

func TestLegacyInputsParamsAreTranslated(t *testing.T) {
	pl, rep := parseYAML(t, pipelineWith(`  params:
  - {name: img, type: string}
  tasks:
  - name: t
    params:
    - {name: image, value: "$(params.img)"}
    taskSpec:
      params:
      - {name: image}
      steps:
      - name: s
        image: alpine
        script: echo $(inputs.params.image)
`))
	j := job(t, pl, "t")
	s := step(t, j, "s")
	if len(s.Script) != 1 || strings.Contains(s.Script[0], "inputs.params") || !strings.Contains(s.Script[0], "img") {
		t.Fatalf("script = %q", s.Script)
	}
	if got := ext(j.Ext)[tekton.ExtLegacyParams]; !reflect.DeepEqual(got, []any{"image"}) {
		t.Fatalf("legacy params = %v", got)
	}
	if hasEntry(rep, "is a Tekton variable with no IR reference") {
		t.Fatalf("a translated reference needs no entry: %+v", rep.Entries)
	}
}

func TestLegacySpellingAndParamNamesSurviveTheTektonTrip(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"p.yaml": pipelineWith(`  params:
  - {name: img, type: string}
  tasks:
  - name: t
    params:
    - {name: image, value: "$(params.img)"}
    taskSpec:
      params:
      - {name: image}
      steps:
      - name: s
        image: alpine
        script: echo $(inputs.params.image)
`)})
	files, _, err := tekton.New().Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, b := range files {
		all.Write(b)
	}
	for _, want := range []string{"echo $(inputs.params.image)", "- name: image\n          value: $(params.img)"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, all.String())
		}
	}
}

const sourceWSPipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
spec:
  workspaces:
  - name: ws
  tasks:
  - name: fetch
    taskRef: {name: git-clone}
    workspaces:
    - {name: output, workspace: ws}
  - name: use
    runAfter: [fetch]
    taskRef: {name: use-src}
    workspaces:
    - {name: src, workspace: ws}
`

const sourceWSTask = `apiVersion: tekton.dev/v1
kind: Task
metadata:
  name: use-src
spec:
  workspaces:
  - name: src
  steps:
  - name: a
    image: alpine
    workingDir: /workspace/src/sub
    script: |
      ls $(workspaces.src.path)/x
      cp /workspace/src/conf /workspace/src2/conf
  - name: b
    image: alpine
    workingDir: $(workspaces.src.path)
    command: [ls]
    args: ["$(workspaces.src.path)/y"]
`

func TestSourceWorkspaceMapsToTheCheckoutAndReturnsExactly(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"p.yaml": sourceWSPipeline, "t.yaml": sourceWSTask})
	j := job(t, p.Pipelines[0], "use")
	if len(j.Steps) != 3 || j.Steps[0].Kind != ir.StepAction || j.Steps[0].Action.Repo != "checkout" {
		t.Fatalf("steps = %+v", j.Steps)
	}
	a, b := step(t, j, "a"), step(t, j, "b")
	if a.WorkDir != "sub" || b.WorkDir != "" || strings.Join(a.Script, "\n") != "ls ./x\ncp ./conf /workspace/src2/conf" {
		t.Fatalf("a = %+v, b = %+v", a, b)
	}
	files, _, err := tekton.New().Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range files {
		all.Write(f)
	}
	for _, want := range []string{"workingDir: /workspace/src/sub", "ls $(workspaces.src.path)/x", "cp /workspace/src/conf /workspace/src2/conf", "workingDir: $(workspaces.src.path)"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("Tekton output lacks %q:\n%s", want, all.String())
		}
	}
	if strings.Contains(all.String(), "name: use-checkout") {
		t.Errorf("the synthetic checkout step reached the Tekton file:\n%s", all.String())
	}
	if strings.Contains(all.String(), "use-checkout") {
		t.Errorf("the file names the checkout step that it does not write:\n%s", all.String())
	}
}

func TestWorkspaceWithoutACloneTaskIsNotMapped(t *testing.T) {
	task := strings.Replace(sourceWSPipeline, "taskRef: {name: git-clone}", "taskRef: {name: other}", 1)
	p, _ := parseFiles(t, map[string]string{"p.yaml": task, "t.yaml": sourceWSTask})
	j := job(t, p.Pipelines[0], "use")
	if len(j.Steps) != 2 || step(t, j, "a").WorkDir != "/workspace/src/sub" {
		t.Fatalf("steps = %+v", j.Steps)
	}
}
