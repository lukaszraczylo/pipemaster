package roundtrip_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
)

const woodpeckerPushOnly = "when:\n  - event: push\nsteps:\n  - name: a\n    image: alpine:3.20\n    commands: [echo hi]\n"

// A Woodpecker push is a push of a branch, so a push of a tag must not start the converted pipeline. A GHA push with
// only branches set does not run for a tag (https://docs.github.com/en/actions/writing-workflows/workflow-syntax-for-github-actions),
// and a Concourse git resource with no tag_filter has no version for a tag.
func TestPushOnlySourceDoesNotStartOnATagPush(t *testing.T) {
	for to, want := range map[string]string{
		provider.GHA:       "branches:\n      - '**'",
		provider.Forgejo:   "branches:\n      - '**'",
		provider.Gitea:     "branches:\n      - '**'",
		provider.GitLab:    "$CI_PIPELINE_SOURCE == \"push\" && $CI_COMMIT_BRANCH",
		provider.Drone:     "event:\n    - push",
		provider.Tekton:    "body.ref.startsWith('refs/heads/')",
		provider.Argo:      "^refs/heads/.*$",
		provider.Concourse: "type: git\n    source:\n      uri: ((git-uri))\n",
	} {
		t.Run(to, func(t *testing.T) {
			files, _ := emitFiles(t, provider.Woodpecker, to, map[string]string{".woodpecker/ci.yaml": woodpeckerPushOnly})
			all := joined(files, always)
			if !strings.Contains(all, want) {
				t.Fatalf("no branch-only start condition %q in\n%s", want, all)
			}
			if strings.Contains(all, "tag_filter") || strings.Contains(all, "refs/tags/") {
				t.Fatalf("the output admits a tag:\n%s", all)
			}
		})
	}
}
