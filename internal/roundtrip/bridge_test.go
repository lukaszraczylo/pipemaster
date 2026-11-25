package roundtrip_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/provider"
)

// bridgeSource holds a trigger job and a job that only overrides an included template job.
const bridgeSource = `stages: [build, post]
include:
  - template: Security/SAST.gitlab-ci.yml
build:
  stage: build
  script: [make]
downstream:
  stage: post
  trigger:
    project: acme/omnibus
    branch: main
    strategy: depend
semgrep-sast:
  stage: build
  rules:
    - if: '$CI_COMMIT_BRANCH == "main"'
`

var bridgeTargets = []string{provider.GHA, "woodpecker", "drone", "tekton", "argo"}

// A job with no steps because its work lives in another pipeline or an include must fail loudly, never run true.
func TestBridgeJobFailsLoudly(t *testing.T) {
	for _, to := range bridgeTargets {
		t.Run(to, func(t *testing.T) {
			src := fstest.MapFS{".gitlab-ci.yml": {Data: []byte(bridgeSource)}}
			r := hops(t, src, prov(t, "gitlab"), prov(t, to), false, true)
			var texts []string
			for _, s := range scriptCandidates(t, r.toFiles) {
				if hasFailLoud(s) {
					texts = append(texts, s)
				}
			}
			for _, job := range []string{"job/downstream", "job/semgrep-sast"} {
				found := false
				for _, s := range texts {
					found = found || strings.Contains(s, "step.script ") && strings.Contains(s, job)
				}
				if !found {
					t.Errorf("no failing script for %s; got %d failing scripts", job, len(texts))
				}
			}
			for _, s := range texts {
				if strings.Contains(s, "job/downstream") && !strings.Contains(s, "acme/omnibus") {
					t.Errorf("the failing script does not name the downstream project:\n%s", s)
				}
			}
			for _, d := range diffNodes(r.src, r.back) {
				t.Errorf("A->B->A: %s differs at %s", d.Field, d.Path)
			}
		})
	}
}
