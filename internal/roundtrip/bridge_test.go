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

const pluginSource = `steps:
  - name: notify
    image: plugins/slack
    settings:
      channel: builds
      tags: [a, b]
      webhook:
        from_secret: SLACK_WEBHOOK
`

// A plugin step runs its image entrypoint with the settings as PLUGIN_ variables, and a secret stays a reference.
func TestPluginStepRunsTheImage(t *testing.T) {
	for _, to := range []string{"tekton", "argo"} {
		t.Run(to, func(t *testing.T) {
			src := fstest.MapFS{".woodpecker/ci.yaml": {Data: []byte(pluginSource)}}
			r := hops(t, src, prov(t, "woodpecker"), prov(t, to), false, true)
			all := ""
			for _, b := range r.toFiles {
				all += string(b)
			}
			for _, want := range []string{"image: plugins/slack", "name: PLUGIN_CHANNEL", "value: builds", "name: PLUGIN_TAGS", "value: a,b", "name: PLUGIN_WEBHOOK", "secretKeyRef"} {
				if !strings.Contains(all, want) {
					t.Errorf("missing %q in\n%s", want, all)
				}
			}
			if strings.Contains(all, "pipemaster: lost: step.action") {
				t.Errorf("the plugin step still fails:\n%s", all)
			}
			if strings.Contains(all, "SLACK_WEBHOOK_VALUE") {
				t.Errorf("a secret value is written")
			}
			for _, d := range diffNodes(r.src, r.back) {
				t.Errorf("A->B->A: %s differs at %s", d.Field, d.Path)
			}
		})
	}
}
