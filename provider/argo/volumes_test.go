package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const sharedClaimWorkflow = `apiVersion: argoproj.io/v1alpha1
kind: WorkflowTemplate
metadata: {name: ci}
spec:
  entrypoint: main
  volumeClaimTemplates:
  - metadata: {name: work}
    spec:
      accessModes: [ReadWriteOnce]
      resources: {requests: {storage: 1Gi}}
  templates:
  - name: main
    dag:
      tasks:
      - {name: produce, template: produce}
      - {name: consume, template: consume, depends: produce}
  - name: produce
    container:
      image: alpine
      command: [sh, -c]
      args: ["echo data > /work/shared.txt"]
      volumeMounts:
      - {name: work, mountPath: /work}
  - name: consume
    container:
      image: alpine
      command: [sh, -c]
      args: ["cat /work/shared.txt"]
      volumeMounts:
      - {name: work, mountPath: /work}
`

func volEntry(rep *report.Report, path string, f caps.Feature, level caps.Level) *report.Entry {
	for i, e := range rep.Entries {
		if e.IRPath == path && e.Feature == f && e.Level == level {
			return &rep.Entries[i]
		}
	}
	return nil
}

func TestClaimBetweenTemplatesBecomesAnArtifact(t *testing.T) {
	pl, rep := parseYAML(t, sharedClaimWorkflow)
	prod, cons := job(t, pl, "produce"), job(t, pl, "consume")
	if len(prod.Artifacts) != 1 || prod.Artifacts[0].Name != "ws-work-produce" || prod.Artifacts[0].Paths[0] != "pm-workspaces/work" {
		t.Fatalf("producer artifacts = %+v", prod.Artifacts)
	}
	if len(cons.Needs) != 1 || !cons.Needs[0].Artifacts {
		t.Fatalf("consumer needs = %+v", cons.Needs)
	}
	if got := strings.Join(prod.Steps[0].Script, "\n"); got != "mkdir -p pm-workspaces/work" {
		t.Errorf("the directory step = %q", got)
	}
	if got := strings.Join(prod.Steps[1].Script, "\n"); got != "sh -c '"+"echo data > pm-workspaces/work/shared.txt"+"'" {
		t.Errorf("producer script = %q", got)
	}
	if got := strings.Join(cons.Steps[1].Script, "\n"); got != "sh -c '"+"cat pm-workspaces/work/shared.txt"+"'" {
		t.Errorf("consumer script = %q", got)
	}
	if volEntry(rep, "pipeline/ci/job/produce", caps.JobArtifacts, caps.Emulated) == nil || volEntry(rep, "pipeline/ci/job/consume", caps.JobNeedsArtifacts, caps.Emulated) == nil {
		t.Errorf("artifact verdicts are missing: %+v", rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && (strings.Contains(e.Detail, "volume") || strings.Contains(e.Detail, "Mounts")) {
			t.Errorf("a mapped volume is reported as lost: %+v", e)
		}
	}
}

func TestClaimBetweenTemplatesReturnsExactly(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"app.yaml": sharedClaimWorkflow})
	files, _ := emit(t, p)
	all := ""
	for _, b := range files {
		all += b
	}
	for _, bad := range []string{"pm-workspaces", "artifacts:"} {
		if strings.Contains(all, bad) {
			t.Errorf("the Argo file holds %q:\n%s", bad, all)
		}
	}
	for _, want := range []string{"echo data > /work/shared.txt", "cat /work/shared.txt", "volumeClaimTemplates"} {
		if !strings.Contains(all, want) {
			t.Errorf("output lacks %q:\n%s", want, all)
		}
	}
	back, _, err := parseRepo(map[string]string{".argo/app.yaml": all})
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}

func TestEmptyDirInOneTemplateIsADirectory(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - {name: scratch, emptyDir: {}}
    container:
      image: alpine
      command: [sh, -c]
      args: ["echo x > /scratch/a && cat /scratch/a"]
      volumeMounts:
      - {name: scratch, mountPath: /scratch}
`)
	pl, rep := parseYAML(t, src)
	j := pl.Jobs[0]
	if got := strings.Join(j.Steps[1].Script, "\n"); got != "sh -c '"+"echo x > pm-workspaces/scratch/a && cat pm-workspaces/scratch/a"+"'" {
		t.Errorf("script = %q", got)
	}
	if len(j.Artifacts) != 0 {
		t.Errorf("an emptyDir stays in one job: %+v", j.Artifacts)
	}
	if volEntry(rep, "pipeline/ci/job/"+j.ID, caps.ExtPassthrough, caps.Emulated) == nil {
		t.Errorf("no emulated verdict: %+v", rep.Entries)
	}
}

func TestSecretVolumeIsExternal(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - name: creds
      secret: {secretName: registry-auth}
    container:
      image: alpine
      command: [sh, -c]
      args: ["cat /etc/creds/config.json"]
      volumeMounts:
      - {name: creds, mountPath: /etc/creds}
`)
	pl, rep := parseYAML(t, src)
	e := volEntry(rep, "pipeline/ci/job/"+pl.Jobs[0].ID, caps.ExtPassthrough, caps.External)
	if e == nil || !strings.Contains(e.Detail, "Secret registry-auth") {
		t.Fatalf("no external verdict that names the Secret: %+v", rep.Entries)
	}
	if got := strings.Join(pl.Jobs[0].Steps[1].Script, "\n"); got != "sh -c '"+"cat pm-workspaces/creds/config.json"+"'" {
		t.Errorf("script = %q", got)
	}
}

func TestUnmountedVolumeStaysLost(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - {name: idle, emptyDir: {}}
    container: {image: alpine, command: [sh, -c], args: ["true"]}
`)
	pl, rep := parseYAML(t, src)
	if volEntry(rep, "pipeline/ci/job/"+pl.Jobs[0].ID, caps.ExtPassthrough, caps.Lost) == nil {
		t.Errorf("an unmounted volume has no verdict: %+v", rep.Entries)
	}
}

func TestMountPathWithinAWordIsNotRewritten(t *testing.T) {
	src := wt(`  - name: main
    volumes:
    - {name: scratch, emptyDir: {}}
    container:
      image: alpine
      command: [sh, -c]
      args: ["ls /scratchy /scratch /scratch/x"]
      volumeMounts:
      - {name: scratch, mountPath: /scratch}
`)
	pl, _ := parseYAML(t, src)
	if got := strings.Join(pl.Jobs[0].Steps[1].Script, "\n"); got != "sh -c '"+"ls /scratchy pm-workspaces/scratch pm-workspaces/scratch/x"+"'" {
		t.Errorf("script = %q", got)
	}
}

func TestClaimReadAcrossAnUnrelatedTemplateGetsTheFilesAndReturnsExactly(t *testing.T) {
	src := strings.Replace(sharedClaimWorkflow, "      - {name: consume, template: consume, depends: produce}\n",
		"      - {name: middle, template: middle, depends: produce}\n      - {name: consume, template: consume, depends: middle}\n", 1)
	src += "  - name: middle\n    container: {image: alpine, command: [sh, -c], args: [\"true\"]}\n"
	p, _ := parseFiles(t, map[string]string{"app.yaml": src})
	cons := job(t, p.Pipelines[0], "consume")
	got := map[string]bool{}
	for _, n := range cons.Needs {
		got[n.JobID] = n.Artifacts
	}
	if len(got) != 2 || got["middle"] || !got["produce"] {
		t.Fatalf("consumer needs = %+v", cons.Needs)
	}
	files, _ := emit(t, p)
	all := ""
	in := map[string]string{}
	for n, b := range files {
		all += b
		in[n] = b
	}
	if strings.Contains(all, "depends: middle && produce") || strings.Contains(all, "produce && middle") {
		t.Errorf("the Argo file holds the added edge:\n%s", all)
	}
	back, _, err := parseRepo(in)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}
