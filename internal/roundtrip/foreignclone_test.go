package roundtrip_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/provider"
)

const cloneImagePin = "alpine/git:v2.54.1@sha256:29cff25811f790b9f72fc266bf8009a5a911714c0e03c31c47339c50909d9aef"

const foreignClonePipeline = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
spec:
  workspaces:
    - name: shared
  tasks:
    - name: fetch
      taskRef:
        name: git-clone
      workspaces:
        - name: output
          workspace: shared
      params:
        - name: url
          value: https://gitlab.com/o/r.git
        - name: revision
          value: v1
    - name: check
      runAfter:
        - fetch
      workspaces:
        - name: ws
          workspace: shared
      taskSpec:
        workspaces:
          - name: ws
        steps:
          - name: read
            image: alpine
            script: |
              cd $(workspaces.ws.path)
              echo "MARK readme:$(test -f README.md && echo yes)"
`

// TestForeignCloneRunsInAShellOfThePinnedImage shows, per target, that the clone script of another repository
// never runs as an argument of the image entrypoint (git), and that no job also checks out the pipeline repository.
func TestForeignCloneRunsInAShellOfThePinnedImage(t *testing.T) {
	src := fstest.MapFS{".tekton/pipeline.yaml": {Data: []byte(foreignClonePipeline)}}
	proj, _, err := prov(t, provider.Tekton).Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	// want holds, per target, the text that makes a shell run the script in the pinned image.
	want := map[string][]string{
		provider.GHA:        {"uses: docker://" + cloneImagePin, "entrypoint: /bin/sh"},
		provider.GitLab:     {"name: " + cloneImagePin, `entrypoint:` + "\n      - \"\""},
		provider.Drone:      {"image: " + cloneImagePin, "commands:"},
		provider.Woodpecker: {"image: " + cloneImagePin, "commands:"},
		provider.Argo:       {"image: " + cloneImagePin, "- sh\n"},
		provider.Concourse:  {"repository: alpine/git", "tag: v2.54.1"},
	}
	for to, needles := range want {
		t.Run(to, func(t *testing.T) {
			out, _, err := prov(t, to).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			for _, data := range out {
				sb.Write(data)
			}
			text := sb.String()
			for _, n := range needles {
				if !strings.Contains(text, n) {
					t.Errorf("%s output lacks %q:\n%s", to, n, text)
				}
			}
			if !strings.Contains(text, `git remote add origin "https://gitlab.com/o/r.git"`) {
				t.Errorf("%s output does not clone the url", to)
			}
			for _, bad := range []string{"actions/checkout", "PIPEMASTER_CHECKOUT_"} {
				if strings.Contains(text, bad) {
					t.Errorf("%s output also checks out a repository through %q:\n%s", to, bad, text)
				}
			}
		})
	}
}
