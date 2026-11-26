package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
	"github.com/lukaszraczylo/pipemaster/report"
)

const twoTaskWorkspace = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
spec:
  workspaces:
  - name: shared
  tasks:
  - name: produce
    workspaces:
    - {name: out, workspace: shared}
    taskSpec:
      workspaces:
      - name: out
      steps:
      - name: write
        image: alpine
        script: |
          echo data > $(workspaces.out.path)/shared.txt
  - name: consume
    runAfter: [produce]
    workspaces:
    - {name: in, workspace: shared}
    taskSpec:
      workspaces:
      - name: in
      steps:
      - name: read
        image: alpine
        script: |
          cat $(workspaces.in.path)/shared.txt
`

func entryAt(rep *report.Report, path string, f caps.Feature, level caps.Level) *report.Entry {
	for i, e := range rep.Entries {
		if e.IRPath == path && e.Feature == f && e.Level == level {
			return &rep.Entries[i]
		}
	}
	return nil
}

func TestWorkspaceBetweenTasksBecomesAnArtifact(t *testing.T) {
	pl, rep := parseYAML(t, twoTaskWorkspace)
	prod, cons := job(t, pl, "produce"), job(t, pl, "consume")
	if len(prod.Artifacts) != 1 || prod.Artifacts[0].Name != "ws-shared" || prod.Artifacts[0].Paths[0] != "pm-workspaces/shared" {
		t.Fatalf("producer artifacts = %+v", prod.Artifacts)
	}
	if len(cons.Needs) != 1 || !cons.Needs[0].Artifacts {
		t.Fatalf("consumer needs = %+v", cons.Needs)
	}
	if got := step(t, prod, "write").Script[0]; got != "echo data > pm-workspaces/shared/shared.txt" {
		t.Errorf("producer script = %q", got)
	}
	if got := step(t, cons, "read").Script[0]; got != "cat pm-workspaces/shared/shared.txt" {
		t.Errorf("consumer script = %q", got)
	}
	if entryAt(rep, "pipeline/ci/job/produce", caps.JobArtifacts, caps.Emulated) == nil || entryAt(rep, "pipeline/ci/job/consume", caps.JobNeedsArtifacts, caps.Emulated) == nil {
		t.Errorf("artifact verdicts are missing: %+v", rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && strings.Contains(e.Detail, "workspace") {
			t.Errorf("a mapped workspace is reported as lost: %+v", e)
		}
	}
}

func TestWorkspaceBetweenTasksReturnsExactly(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"p.yaml": twoTaskWorkspace})
	files, _, err := tekton.New().Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	in := map[string]string{}
	for n, b := range files {
		all.Write(b)
		in[n] = string(b)
	}
	for _, bad := range []string{"pipemaster-artifacts", "pipemaster-workspace", "pm-workspaces"} {
		if strings.Contains(all.String(), bad) {
			t.Errorf("the Tekton file holds %q:\n%s", bad, all.String())
		}
	}
	for _, want := range []string{"echo data > $(workspaces.out.path)/shared.txt", "cat $(workspaces.in.path)/shared.txt"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, all.String())
		}
	}
	back, _, err := tekton.New().Parse(repo(in))
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}

func TestWorkspaceReadAcrossAnUnrelatedTaskIsLost(t *testing.T) {
	src := strings.Replace(twoTaskWorkspace, "  - name: consume\n    runAfter: [produce]\n", "  - name: middle\n    runAfter: [produce]\n    taskSpec:\n      steps:\n      - {name: m, image: alpine, script: 'true'}\n  - name: consume\n    runAfter: [middle]\n", 1)
	_, rep := parseYAML(t, src)
	e := entryAt(rep, "pipeline/ci/job/consume", caps.JobNeedsArtifacts, caps.Lost)
	if e == nil || !strings.Contains(e.Detail, "do not reach this task") {
		t.Fatalf("no lost verdict for the unlinked read: %+v", rep.Entries)
	}
}

func TestWorkspaceBoundToASecretIsExternal(t *testing.T) {
	run := `apiVersion: tekton.dev/v1
kind: PipelineRun
metadata:
  name: run
spec:
  pipelineRef: {name: ci}
  workspaces:
  - name: shared
    secret: {secretName: ci-creds}
`
	p, rep := parseFiles(t, map[string]string{"p.yaml": twoTaskWorkspace, "run.yaml": run})
	if len(p.Pipelines[0].Jobs[0].Artifacts) != 0 {
		t.Errorf("a Secret workspace is read only, so no artifact is made: %+v", p.Pipelines[0].Jobs[0].Artifacts)
	}
	e := entryAt(rep, "pipeline/ci/job/produce", caps.ExtPassthrough, caps.External)
	if e == nil || !strings.Contains(e.Detail, "Secret ci-creds") {
		t.Fatalf("no external verdict that names the Secret: %+v", rep.Entries)
	}
}

func TestDockerconfigWorkspaceIsExternal(t *testing.T) {
	src := strings.ReplaceAll(twoTaskWorkspace, "name: out", "name: dockerconfig")
	src = strings.ReplaceAll(src, "{name: out,", "{name: dockerconfig,")
	src = strings.ReplaceAll(src, "$(workspaces.out.path)", "$(workspaces.dockerconfig.path)")
	_, rep := parseYAML(t, src)
	e := entryAt(rep, "pipeline/ci/job/produce", caps.ExtPassthrough, caps.External)
	if e == nil || !strings.Contains(e.Detail, "registry credentials") {
		t.Fatalf("no external verdict for dockerconfig: %+v", rep.Entries)
	}
}

func TestWorkspaceSubPathMapsToASubdirectory(t *testing.T) {
	src := strings.Replace(twoTaskWorkspace, "{name: out, workspace: shared}", "{name: out, workspace: shared, subPath: app}", 1)
	pl, _ := parseYAML(t, src)
	if got := step(t, job(t, pl, "produce"), "write").Script[0]; got != "echo data > pm-workspaces/shared/app/shared.txt" {
		t.Errorf("script = %q", got)
	}
}

func TestWorkspaceSubPathThatEscapesIsNotMapped(t *testing.T) {
	src := strings.Replace(twoTaskWorkspace, "{name: out, workspace: shared}", "{name: out, workspace: shared, subPath: ../x}", 1)
	pl, rep := parseYAML(t, src)
	if got := step(t, job(t, pl, "produce"), "write").Script[0]; strings.Contains(got, "pm-workspaces") {
		t.Errorf("an escaping subPath was mapped: %q", got)
	}
	if entryAt(rep, "pipeline/ci/job/produce", caps.ExtPassthrough, caps.Lost) == nil {
		t.Errorf("the unmapped workspace has no verdict: %+v", rep.Entries)
	}
}
