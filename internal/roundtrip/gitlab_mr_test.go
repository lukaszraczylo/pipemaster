package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mrFixture = "../../testdata/gitlab/features/job.if--merge-request-rule"

// convertMR converts the merge request fixture to target and returns the output directory and the report text.
func convertMR(t *testing.T, target string) (string, string) {
	t.Helper()
	out := t.TempDir()
	code, _, msg := runOut("convert", mrFixture, "--from", "gitlab", "--to", target, "-o", out)
	if code != 0 {
		t.Fatalf("convert to %s = %d\n%s", target, code, msg)
	}
	return out, msg
}

func readFile(t *testing.T, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(parts...)) // #nosec G304 -- a path under the test output directory
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A GitLab merge request pipeline has no closed or metadata run, so a Woodpecker workflow for it must not list them.
func TestGitLabMergeRequestRuleWoodpeckerHasNoClosedEvents(t *testing.T) {
	out, _ := convertMR(t, "woodpecker")
	wf := readFile(t, out, ".woodpecker", "mrjob.yaml")
	if !strings.Contains(wf, "pull_request") {
		t.Fatalf("the merge request job has no pull_request event:\n%s", wf)
	}
	for _, ev := range []string{"pull_request_closed", "pull_request_metadata"} {
		if strings.Contains(wf, ev) {
			t.Errorf("the workflow lists %s, which GitLab never runs:\n%s", ev, wf)
		}
	}
	if plain := readFile(t, out, ".woodpecker", "build.yaml"); strings.Contains(plain, "pull_request") {
		t.Errorf("the job with no rules runs on a pull request:\n%s", plain)
	}
}

// The default triggers of GitLab give a manual start and a pull request run for a job that names the merge request
// source. The jobs with no rules stay off the pull request run.
func TestGitLabMergeRequestRuleGHATriggers(t *testing.T) {
	out, msg := convertMR(t, "gha")
	wf := readFile(t, out, ".github", "workflows", "gitlab-ci.yml")
	for _, want := range []string{"workflow_dispatch:", "pull_request:", "if: '!(github.event_name == ''pull_request'')"} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q:\n%s", want, wf)
		}
	}
	if !strings.Contains(wf, "if: github.event_name == 'pull_request'") {
		t.Errorf("the merge request job lost its condition:\n%s", wf)
	}
	if !strings.Contains(msg, "trigger.schedule") {
		t.Errorf("the schedule source has no report entry:\n%s", msg)
	}
}

func TestGitLabDefaultTriggersTektonArgoGetManual(t *testing.T) {
	for _, target := range []string{"tekton", "argo"} {
		t.Run(target, func(t *testing.T) {
			_, msg := convertMR(t, target)
			for _, want := range []string{"trigger.manual", "trigger.pull_request", "trigger.schedule"} {
				if !strings.Contains(msg, want) {
					t.Errorf("the report has no %s entry:\n%s", want, msg)
				}
			}
		})
	}
}

// Concourse has no pull request event, so the merge request job never runs there and the report must say so.
func TestGitLabMergeRequestRuleConcourseReportsTheEvent(t *testing.T) {
	_, msg := convertMR(t, "concourse")
	for _, line := range strings.Split(msg, "\n") {
		if strings.Contains(line, "trigger.pull_request") && strings.Contains(line, "merge_request_event") && strings.HasPrefix(strings.TrimSpace(line), "[lost]") {
			return
		}
	}
	t.Errorf("the report has no lost trigger.pull_request entry for merge_request_event:\n%s", msg)
}
