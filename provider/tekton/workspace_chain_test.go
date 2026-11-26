package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

const threeTaskWorkspace = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
spec:
  workspaces:
  - name: shared
  tasks:
  - name: first
    workspaces:
    - {name: ws, workspace: shared}
    taskSpec:
      workspaces:
      - name: ws
      steps:
      - {name: a, image: alpine, script: 'echo one > $(workspaces.ws.path)/f.txt'}
  - name: second
    runAfter: [first]
    workspaces:
    - {name: ws, workspace: shared}
    taskSpec:
      workspaces:
      - name: ws
      steps:
      - {name: b, image: alpine, script: 'echo two >> $(workspaces.ws.path)/f.txt'}
  - name: third
    runAfter: [second]
    workspaces:
    - {name: ws, workspace: shared}
    taskSpec:
      workspaces:
      - name: ws
      steps:
      - {name: c, image: alpine, script: 'cat $(workspaces.ws.path)/f.txt'}
`

func TestEveryWriterOfAChainUploadsItsOwnArtifactName(t *testing.T) {
	proj, _ := parseFiles(t, map[string]string{"p.yaml": threeTaskWorkspace})
	pl := proj.Pipelines[0]
	a, b := job(t, pl, "first"), job(t, pl, "second")
	if len(a.Artifacts) != 1 || len(b.Artifacts) != 1 || a.Artifacts[0].Name == b.Artifacts[0].Name {
		t.Fatalf("writers share an artifact name: %+v %+v", a.Artifacts, b.Artifacts)
	}
	if got := needSet(job(t, pl, "second")); len(got) != 1 || !got["first/true"] {
		t.Errorf("second needs = %+v", job(t, pl, "second").Needs)
	}
	if got := needSet(job(t, pl, "third")); len(got) != 1 || !got["second/true"] {
		t.Errorf("third needs = %+v", job(t, pl, "third").Needs)
	}
	files, _, err := gha.New(gha.GHA).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range files {
		all.Write(f)
	}
	out := all.String()
	if n := strings.Count(out, "actions/upload-artifact"); n != 2 {
		t.Errorf("want two uploads, got %d:\n%s", n, out)
	}
	for _, name := range []string{"name: ws-shared-first", "name: ws-shared-second"} {
		if !strings.Contains(out, name) {
			t.Errorf("missing %q:\n%s", name, out)
		}
	}
}
