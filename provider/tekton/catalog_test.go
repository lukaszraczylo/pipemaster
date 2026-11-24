package tekton_test

import (
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
		{name: "kaniko by name", task: "    taskRef: {name: kaniko}\n", wantStep: ir.StepScript, wantImg: "gcr.io/kaniko-project/executor:v1.5.1-debug", mapped: true},
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
			if len(j.Steps) == 0 || j.Steps[0].Kind != tt.wantStep {
				t.Fatalf("steps = %+v, want kind %s", j.Steps, tt.wantStep)
			}
			img := j.Container
			if img == nil {
				img = j.Steps[0].Image
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
	st := job(t, p.Pipelines[0], "t").Steps[0]
	if len(st.Script) == 0 || !strings.Contains(strings.Join(st.Script, "\n"), "yamllint -d relaxed .") {
		t.Fatalf("script = %q", st.Script)
	}
}
