package tekton_test

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"strings"
	"testing"
)

const twoCloneWorkspaces = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
spec:
  workspaces:
    - {name: app}
    - {name: lib}
  tasks:
    - name: fetch-app
      taskRef:
        resolver: cluster
        params:
          - {name: kind, value: task}
          - {name: name, value: git-clone}
      workspaces:
        - {name: output, workspace: app}
      params:
        - {name: URL, value: https://github.com/example/app}
    - name: fetch-lib
      taskRef:
        resolver: cluster
        params:
          - {name: kind, value: task}
          - {name: name, value: git-clone}
      workspaces:
        - {name: output, workspace: lib}
      params:
        - {name: URL, value: https://gitlab.com/example/lib}
    - name: check
      runAfter: [fetch-app, fetch-lib]
      workspaces:
        - {name: a, workspace: app}
        - {name: b, workspace: lib}
      taskSpec:
        workspaces:
          - name: a
          - name: b
        steps:
          - name: ls
            image: alpine
            script: ls $(workspaces.a.path) $(workspaces.b.path)
`

func TestSecondSourceWorkspaceIsReportedLost(t *testing.T) {
	_, rep := parseYAML(t, twoCloneWorkspaces)
	var lost []string
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && strings.Contains(e.IRPath, "check") {
			lost = append(lost, e.Detail)
		}
	}
	if len(lost) != 1 || !strings.Contains(lost[0], "lib") {
		t.Errorf("want one lost entry naming workspace lib on job check, got %q", lost)
	}
}
