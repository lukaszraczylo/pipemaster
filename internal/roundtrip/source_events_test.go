package roundtrip_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

const sourcesGitLab = `job_web:
  script: [echo web]
  rules:
    - if: '$CI_PIPELINE_SOURCE == "web"'
job_sched:
  script: [echo sched]
  rules:
    - if: '$CI_PIPELINE_SOURCE == "schedule"'
job_api:
  script: [echo api]
  rules:
    - if: '$CI_PIPELINE_SOURCE == "api" || $CI_PIPELINE_SOURCE == "trigger"'
`

func lostJobIf(rep *report.Report) bool {
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && (e.Feature == caps.JobIf || e.Feature == caps.StepIf) {
			return true
		}
	}
	return false
}

// A GitLab job that runs on the web, schedule or API sources gets the GHA event that starts it, and no job is
// disabled: the workflow names the events, and the schedule that GitLab keeps in the project settings is reported.
func TestGitLabSourcesStartGHAJobs(t *testing.T) {
	out, rep := convert(t, "gitlab", "gha", ".gitlab-ci.yml", sourcesGitLab)
	for _, want := range []string{
		"workflow_dispatch:", "repository_dispatch:", "- api", "- trigger", "schedule:", "- cron: 0 0 31 2 *",
		"github.event_name == 'workflow_dispatch'", "github.event_name == 'schedule'",
		"github.event_name == 'repository_dispatch' && github.event.action == 'api'",
		"github.event_name == 'repository_dispatch' && github.event.action == 'trigger'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "if: false") || lostJobIf(rep) {
		t.Errorf("a job was disabled:\n%s\n%+v", out, rep.Entries)
	}
	var external bool
	for _, e := range rep.Entries {
		external = external || e.Level == caps.External && e.Feature == caps.TriggerSchedule
	}
	if !external {
		t.Errorf("the schedule that lives in the project settings is not reported as external: %+v", rep.Entries)
	}
}

// A job whose condition names no API source adds no event the source never named.
func TestGitLabDefaultSourcesAddOnlyNamedEvents(t *testing.T) {
	out, _ := convert(t, "gitlab", "gha", ".gitlab-ci.yml", "build:\n  script: [make]\n  rules:\n    - if: '$CI_COMMIT_BRANCH == \"main\"'\n")
	for _, no := range []string{"repository_dispatch", "schedule:", "workflow_dispatch"} {
		if strings.Contains(out, no) {
			t.Errorf("%s added for a job that tests a branch only:\n%s", no, out)
		}
	}
}

// A GitLab job that runs on a push starts on a branch push and a tag push, which Woodpecker reports as two events.
func TestGitLabPushJobRunsOnWoodpeckerPushAndTag(t *testing.T) {
	src := "job_push:\n  script: [echo push]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n"
	out, rep := convert(t, "gitlab", "woodpecker", ".gitlab-ci.yml", src)
	if lostJobIf(rep) || strings.Contains(out, `evaluate: "false"`) {
		t.Fatalf("the push job is disabled:\n%s\n%+v", out, rep.Entries)
	}
	if !strings.Contains(out, `CI_PIPELINE_EVENT == "push" || CI_PIPELINE_EVENT == "tag"`) {
		t.Errorf("the push job has no push and tag filter:\n%s", out)
	}
}

// A GHA job that tests a negated push event runs on the other events of its workflow only.
func TestGHANotPushRunsOnTheOtherEventsOnWoodpecker(t *testing.T) {
	src := "on: [push, pull_request]\njobs:\n  a:\n    if: github.event_name != 'push'\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	out, rep := convert(t, "gha", "woodpecker", ".github/workflows/a.yml", src)
	if lostJobIf(rep) {
		t.Fatalf("the job condition is lost:\n%s\n%+v", out, rep.Entries)
	}
	if !strings.Contains(out, "^pull_request") || strings.Contains(out, `== "push"`) || strings.Contains(out, `== "tag"`) {
		t.Errorf("the filter does not exclude push and tag:\n%s", out)
	}
}

// A source that starts no run by an event gets a Drone event list, because a Drone document with no trigger runs on every
// event, and the file reads back as a pipeline with no trigger.
func TestEventlessSourceGetsADroneEventList(t *testing.T) {
	src := "apiVersion: tekton.dev/v1\nkind: Pipeline\nmetadata:\n  name: ci\nspec:\n  tasks:\n    - name: build\n      taskSpec:\n        steps:\n          - name: s\n            image: alpine\n            script: echo ok\n"
	out, _ := convert(t, "tekton", "drone", ".tekton/ci.yaml", src)
	for _, want := range []string{"trigger:", "- push", "- pull_request", "- tag"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for _, no := range []string{"cron", "custom", "promote", "rollback"} {
		if strings.Contains(out, "- "+no) {
			t.Errorf("event %s is admitted:\n%s", no, out)
		}
	}
}
