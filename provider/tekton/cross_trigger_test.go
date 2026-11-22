package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

const celGuarded = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata: {name: deploy}
spec:
  tasks:
  - {name: d, taskSpec: {steps: [{name: s, image: alpine, script: "echo deploy-to-prod"}]}}
---
apiVersion: triggers.tekton.dev/v1beta1
kind: EventListener
metadata: {name: el}
spec:
  triggers:
  - name: main-only
    interceptors:
    - ref: {name: github}
      params:
      - {name: eventTypes, value: [push]}
    - ref: {name: cel}
      params:
      - {name: filter, value: "body.ref == 'refs/heads/main'"}
    bindings: [{ref: b}]
    template: {ref: t}
---
apiVersion: triggers.tekton.dev/v1beta1
kind: TriggerTemplate
metadata: {name: t}
spec:
  resourcetemplates:
  - apiVersion: tekton.dev/v1
    kind: PipelineRun
    metadata: {generateName: deploy-}
    spec:
      pipelineRef: {name: deploy}
`

// A CEL filter that has no IR form must not turn into an unfiltered push trigger in another provider.
func TestCELFilterNeverWidensInOtherProviders(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"all.yaml": celGuarded})
	targets := map[string]provider.Provider{
		"gha": gha.New(gha.GHA), "gitlab": gitlab.New(), "drone": drone.New(), "woodpecker": woodpecker.New(),
	}
	for name, target := range targets {
		t.Run(name, func(t *testing.T) {
			files, _, err := target.Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			for path, b := range files {
				if strings.Contains(path, ".pipemaster/") {
					continue
				}
				if strings.Contains(string(b), `evaluate: "false"`) {
					continue
				}
				for _, bad := range []string{"push", "CI_COMMIT_BRANCH", "pull_request"} {
					if strings.Contains(string(b), bad) {
						t.Errorf("%s holds %q, an unfiltered trigger:\n%s", path, bad, b)
					}
				}
			}
		})
	}
}

func TestCELFilterTriggerStaysInTektonRoundTrip(t *testing.T) {
	files, _ := identityOf(t, map[string]string{"all.yaml": celGuarded})
	if !strings.Contains(joinFiles(files), "body.ref == 'refs/heads/main'") {
		t.Fatalf("filter lost:\n%s", joinFiles(files))
	}
}
