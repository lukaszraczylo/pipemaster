package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

const wsEnvPipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
spec:
  params:
  - name: sub
  workspaces:
  - name: shared
  tasks:
  - name: first
    workspaces:
    - {name: ws, workspace: shared}
    params:
    - {name: sub, value: $(params.sub)}
    taskSpec:
      params:
      - name: sub
      workspaces:
      - name: ws
      stepTemplate:
        env:
        - {name: TPL, value: "$(workspaces.ws.path)/tpl"}
      sidecars:
      - name: side
        image: alpine
        env:
        - {name: SIDE, value: "$(workspaces.ws.path)/side"}
      steps:
      - name: expr
        image: alpine
        env:
        - {name: MIXED, value: "$(params.sub)/$(workspaces.ws.path)/x"}
        script: 'true'
      - name: dir
        image: alpine
        workingDir: /workspace/ws/app
        script: 'true'
`

func lostAt(rep *report.Report, f caps.Feature, substr string) *report.Entry {
	for i, e := range rep.Entries {
		if e.Feature == f && e.Level == caps.Lost && strings.Contains(e.Detail, substr) {
			return &rep.Entries[i]
		}
	}
	return nil
}

func TestUnconvertedWorkspaceEnvHasAVerdictThatNamesTheEffect(t *testing.T) {
	_, rep := parseYAML(t, wsEnvPipeline)
	for _, tc := range []struct {
		f      caps.Feature
		detail string
	}{
		{caps.StepEnv, "env MIXED holds the path of workspace ws"},
		{caps.JobServices, "env SIDE holds the path of workspace ws"},
		{caps.StepEnv, "the job env TPL holds a workspace path relative to the checkout root"},
	} {
		e := lostAt(rep, tc.f, tc.detail)
		if e == nil {
			t.Errorf("no lost entry for %q: %+v", tc.detail, rep.Entries)
		} else if !strings.Contains(e.Detail, "resolves it under") && !strings.Contains(e.Detail, "does not point at the directory") {
			t.Errorf("the entry does not name the effect: %q", e.Detail)
		}
	}
}
