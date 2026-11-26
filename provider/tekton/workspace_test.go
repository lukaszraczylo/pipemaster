package tekton_test

import (
	"slices"
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
	if got := step(t, prod, "produce-workspace-dirs").Script[0]; got != "mkdir -p pm-workspaces/shared" {
		t.Errorf("the directory step = %q", got)
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

const middleTask = "  - name: middle\n    runAfter: [produce]\n    taskSpec:\n      steps:\n      - {name: m, image: alpine, script: 'true'}\n  - name: consume\n    runAfter: [middle]\n"

func needSet(j *ir.Job) map[string]bool {
	out := map[string]bool{}
	for _, n := range j.Needs {
		out[n.JobID+"/"+fmtAny(n.Artifacts)] = true
	}
	return out
}

func TestWorkspaceReadAcrossAnUnrelatedTaskGetsTheFiles(t *testing.T) {
	src := strings.Replace(twoTaskWorkspace, "  - name: consume\n    runAfter: [produce]\n", middleTask, 1)
	pl, _ := parseYAML(t, src)
	cons := job(t, pl, "consume")
	if got := needSet(cons); !got["middle/false"] || !got["produce/true"] || len(got) != 2 {
		t.Fatalf("consumer needs = %+v", cons.Needs)
	}
	if len(job(t, pl, "produce").Artifacts) != 1 {
		t.Errorf("the writer has no artifact: %+v", job(t, pl, "produce").Artifacts)
	}
	if len(job(t, pl, "middle").Needs) != 1 || job(t, pl, "middle").Needs[0].Artifacts {
		t.Errorf("the task that does not bind the workspace changed: %+v", job(t, pl, "middle").Needs)
	}
}

func TestWorkspaceNeedAddsNoOrderAndReturnsExactly(t *testing.T) {
	src := strings.Replace(twoTaskWorkspace, "  - name: consume\n    runAfter: [produce]\n", middleTask, 1)
	p, _ := parseFiles(t, map[string]string{"p.yaml": src})
	pl := p.Pipelines[0]
	before := map[string]string{}
	for _, j := range pl.Jobs {
		before[j.ID] = ancestors(pl, j.ID)
	}
	if before["consume"] != "middle,produce" {
		t.Fatalf("ancestors of consume = %q", before["consume"])
	}
	files, _, err := tekton.New().Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{}
	var all strings.Builder
	for n, b := range files {
		in[n] = string(b)
		all.Write(b)
	}
	if strings.Contains(all.String(), "- middle\n                - produce") {
		t.Errorf("the Tekton file holds the added edge:\n%s", all.String())
	}
	file := back0(t, in).Pipelines[0]
	for _, j := range file.Jobs {
		if ancestors(file, j.ID) != before[j.ID] {
			t.Errorf("order of %s changed", j.ID)
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

// ancestors lists the jobs that a job runs after, directly or not.
func ancestors(pl *ir.Pipeline, id string) string {
	needs := map[string][]string{}
	for _, j := range pl.Jobs {
		for _, n := range j.Needs {
			needs[j.ID] = append(needs[j.ID], n.JobID)
		}
	}
	seen := map[string]bool{}
	var walk func(string)
	walk = func(x string) {
		for _, n := range needs[x] {
			if !seen[n] {
				seen[n] = true
				walk(n)
			}
		}
	}
	walk(id)
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)
	return strings.Join(out, ",")
}

func TestWorkspaceChainDownloadsFromTheLatestWriterOnly(t *testing.T) {
	src := strings.Replace(twoTaskWorkspace, "  - name: consume\n    runAfter: [produce]\n", "  - name: second\n    runAfter: [produce]\n    workspaces:\n    - {name: out, workspace: shared}\n    taskSpec:\n      workspaces:\n      - name: out\n      steps:\n      - {name: w, image: alpine, script: 'echo more >> $(workspaces.out.path)/shared.txt'}\n  - name: consume\n    runAfter: [second]\n", 1)
	pl, _ := parseYAML(t, src)
	if got := needSet(job(t, pl, "consume")); len(got) != 1 || !got["second/true"] {
		t.Errorf("consumer needs = %+v", job(t, pl, "consume").Needs)
	}
}

func TestTwoParallelWritersBothFeedTheReader(t *testing.T) {
	src := strings.Replace(twoTaskWorkspace, "  - name: consume\n    runAfter: [produce]\n", "  - name: produce2\n    workspaces:\n    - {name: out, workspace: shared}\n    taskSpec:\n      workspaces:\n      - name: out\n      steps:\n      - {name: w, image: alpine, script: 'echo two > $(workspaces.out.path)/two.txt'}\n  - name: consume\n    runAfter: [produce, produce2]\n", 1)
	pl, rep := parseYAML(t, src)
	cons := job(t, pl, "consume")
	if got := needSet(cons); len(got) != 2 || !got["produce/true"] || !got["produce2/true"] {
		t.Fatalf("consumer needs = %+v", cons.Needs)
	}
	e := entryAt(rep, "pipeline/ci/job/consume", caps.JobNeedsArtifacts, caps.Emulated)
	if e == nil || !strings.Contains(e.Detail, "run in parallel") {
		t.Errorf("the parallel writers have no caveat: %+v", rep.Entries)
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

func back0(t *testing.T, in map[string]string) *ir.Project {
	t.Helper()
	p, _, err := tekton.New().Parse(repo(in))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
