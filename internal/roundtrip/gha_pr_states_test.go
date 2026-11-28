package roundtrip_test

import (
	"strings"
	"testing"
)

// A GitLab pipeline whose jobs never name the merge request source gets no pull request trigger.
func TestGitLabPlainPipelineGetsNoPullRequestTrigger(t *testing.T) {
	out, _ := convert(t, "gitlab", "gha", ".gitlab-ci.yml", "build:\n  script: [make]\n")
	if strings.Contains(out, "pull_request:") {
		t.Errorf("a pull_request trigger was added:\n%s", out)
	}
	if !strings.Contains(out, "workflow_dispatch:") {
		t.Errorf("the manual trigger is missing:\n%s", out)
	}
}

// A GHA pull request job keeps the closed and metadata states off the Woodpecker file: the GHA types are not mapped,
// so the file lists the one event.
func TestGHAPullRequestJobWoodpeckerHasNoClosedEvents(t *testing.T) {
	src := "on: pull_request\njobs:\n  a:\n    if: github.event_name == 'pull_request'\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	out, _ := convert(t, "gha", "woodpecker", ".github/workflows/a.yml", src)
	if !strings.Contains(out, "pull_request") {
		t.Fatalf("no pull_request event:\n%s", out)
	}
	for _, ev := range []string{"pull_request_closed", "pull_request_metadata"} {
		if strings.Contains(out, ev) {
			t.Errorf("the file lists %s:\n%s", ev, out)
		}
	}
}
