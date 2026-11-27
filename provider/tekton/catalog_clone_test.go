package tekton_test

import (
	"fmt"
	"strings"
	"testing"
)

const catalogForeignClone = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: ci
spec:
  workspaces:
    - name: shared
  params:
    - name: git-url
      type: string
  tasks:
    - name: fetch
      taskRef:
        resolver: cluster
        params:
          - {name: kind, value: task}
          - {name: name, value: git-clone}
          - {name: namespace, value: openshift-pipelines}
      workspaces:
        - {name: output, workspace: shared}
      params:
        - {name: URL, value: $(params.git-url)}
    - name: build-image
      runAfter: [fetch]
      taskRef:
        resolver: cluster
        params:
          - {name: kind, value: task}
          - {name: name, value: buildah}
          - {name: namespace, value: openshift-pipelines}
      params:
        - {name: IMAGE, value: example/app}
      workspaces:
        - {name: source, workspace: shared}
`

func TestCatalogTaskReadsTheForeignClone(t *testing.T) {
	pl, _ := parseYAML(t, catalogForeignClone)
	j := job(t, pl, "build-image")
	if len(j.Steps) == 0 {
		t.Fatal("build-image has no steps")
	}
	first := fmt.Sprintf("%+v", *j.Steps[0])
	if !strings.Contains(first, "git-url") {
		t.Errorf("the first step of build-image does not clone the git-url param: %s", first)
	}
}
