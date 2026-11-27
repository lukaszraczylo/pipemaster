package roundtrip_test

import (
	"strings"
	"testing"
)

const mrStatusFixture = "../../testdata/gitlab/features/job.if--merge-request-status-jobs"

func convertMRStatus(t *testing.T, target string) string {
	t.Helper()
	out := t.TempDir()
	if code, _, msg := runOut("convert", mrStatusFixture, "--from", "gitlab", "--to", target, "-o", out); code != 0 {
		t.Fatalf("convert to %s = %d\n%s", target, code, msg)
	}
	return out
}

// A job with no rules is not in a merge request pipeline, even when its only condition is a status (when: always,
// when: on_failure), so the pull request run must skip it. The trigger admits only the actions GitLab starts on.
func TestGitLabStatusJobsStayOffPullRequestGHA(t *testing.T) {
	wf := readFile(t, convertMRStatus(t, "gha"), ".github", "workflows", "gitlab-ci.yml")
	for _, want := range []string{
		"if: always() && !(github.event_name == 'pull_request') && !github.event.deleted",
		"if: always() && (!(github.event_name == 'pull_request') && failure()) && !github.event.deleted",
		"- opened\n      - synchronize",
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow lacks %q:\n%s", want, wf)
		}
	}
	if strings.Contains(wf, "reopened") {
		t.Errorf("the pull request trigger admits reopened:\n%s", wf)
	}
}

// The gate is a task when on Tekton, so a gated task does not start on a pull request, and the action filter has no
// reopened.
func TestGitLabStatusJobsStayOffPullRequestTekton(t *testing.T) {
	out := convertMRStatus(t, "tekton")
	pl := readFile(t, out, ".tekton", "pipeline.yaml")
	if n := strings.Count(pl, "operator: notin"); n < 3 {
		t.Errorf("want a notin when on build, cleanup and onfail, got %d:\n%s", n, pl)
	}
	el := readFile(t, out, ".tekton", "triggers", "eventlistener.yaml")
	if !strings.Contains(el, "body.action in ['opened','synchronize'] &&") {
		t.Errorf("the action filter is not opened and synchronize:\n%s", el)
	}
}

func TestGitLabStatusJobsStayOffPullRequestArgo(t *testing.T) {
	out := convertMRStatus(t, "argo")
	wf := readFile(t, out, ".argo", "workflows", "gitlab-ci.yaml")
	if n := strings.Count(wf, "!(workflow.parameters[''ctx-event''] == ''pull_request'')"); n < 3 {
		t.Errorf("want the gate on build, cleanup and onfail, got %d:\n%s", n, wf)
	}
	if s := readFile(t, out, ".argo", "events", "sensor.yaml"); strings.Contains(s, "reopened") {
		t.Errorf("the sensor admits reopened:\n%s", s)
	}
}
