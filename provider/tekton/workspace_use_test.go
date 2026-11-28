package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/tekton"
)

const wsUsePipeline = `apiVersion: tekton.dev/v1
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
      - name: write
        image: alpine
        script: 'echo one > $(workspaces.ws.path)/f.txt'
  - name: second
    runAfter: [first]
    workspaces:
    - {name: ws, workspace: shared}
    taskSpec:
      workspaces:
      - name: ws
      stepTemplate:
        env:
        - {name: TPL, value: "$(workspaces.ws.path)/tpl"}
      steps:
      - name: cmd
        image: alpine
        command: [cat]
        args: ["$(workspaces.ws.path)/f.txt"]
        env:
        - {name: CONF, value: "$(workspaces.ws.path)/conf"}
      - name: dir
        image: alpine
        workingDir: /workspace/ws/app
        script: 'cat $(workspaces.ws.path)/f.txt'
`

func TestWorkspacePathReachesCommandArgsEnvAndWorkDir(t *testing.T) {
	proj, _ := parseFiles(t, map[string]string{"p.yaml": wsUsePipeline})
	second := job(t, proj.Pipelines[0], "second")
	cmd := step(t, second, "cmd")
	if got := strings.Join(cmd.Script, "\n"); got != "cat pm-workspaces/shared/f.txt" {
		t.Errorf("args = %q", got)
	}
	if len(cmd.Env) != 1 || cmd.Env[0].Value.Lit != "pm-workspaces/shared/conf" {
		t.Errorf("step env = %+v", cmd.Env)
	}
	if len(second.Env) != 1 || second.Env[0].Value.Lit != "pm-workspaces/shared/tpl" {
		t.Errorf("job env = %+v", second.Env)
	}
	dir := step(t, second, "dir")
	if dir.WorkDir != "pm-workspaces/shared/app" || strings.Join(dir.Script, "\n") != "cat ../../../pm-workspaces/shared/f.txt" {
		t.Errorf("workdir step = %q %q", dir.WorkDir, dir.Script)
	}
	files, _, err := tekton.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{}
	var all strings.Builder
	for n, b := range files {
		in[n] = string(b)
		all.Write(b)
	}
	for _, want := range []string{"$(workspaces.ws.path)/conf", "$(workspaces.ws.path)/tpl", "$(workspaces.ws.path)/f.txt"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, all.String())
		}
	}
	back, _, err := tekton.New().Parse(repo(in))
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(proj, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}
