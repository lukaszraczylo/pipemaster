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
		"workflow_dispatch:", "repository_dispatch:", "- api", "- trigger",
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
	if strings.Contains(out, "cron:") || strings.Contains(out, "\n  schedule:") {
		t.Errorf("the workflow holds a schedule that no GitLab schedule named:\n%s", out)
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

const branchTagGitLab = `branch_only:
  script: [echo branch]
  rules:
    - if: '$CI_COMMIT_BRANCH == "main"'
tag_only:
  script: [echo tag]
  rules:
    - if: '$CI_COMMIT_TAG == "v1"'
`

// A tag push gives a branch test no branch: Tekton and Argo tell refs/tags/ from refs/heads/ in the webhook ref, so a
// branch-only job never starts on a tag push, and a tag name never reads as a branch name.
func TestTektonAndArgoTagPushHasNoBranch(t *testing.T) {
	t.Run("tekton", func(t *testing.T) {
		out, _ := convertText(t, "gitlab", "tekton", ".gitlab-ci.yml", branchTagGitLab)
		for _, want := range []string{
			"body.ref.startsWith('refs/heads/')", "body.ref.startsWith('refs/tags/')",
			"key: ctx_branch\n                  expression: ''''''",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "ctx_branch\n                  expression: body.ref.replace('refs/tags/'") {
			t.Errorf("a tag push sets the branch:\n%s", out)
		}
	})
	t.Run("argo", func(t *testing.T) {
		out, _ := convertText(t, "gitlab", "argo", ".gitlab-ci.yml", branchTagGitLab)
		const tagName = `trimPrefix "refs/tags/" .Input.body.ref`
		if got := strings.Count(out, tagName); got != 1 {
			t.Errorf("the tag trigger reads the tag name %d times, want 1 (the tag param only):\n%s", got, out)
		}
		if !strings.Contains(out, "spec.arguments.parameters.1.value\n              src:\n                dataTemplate: '{{ "+tagName) {
			t.Errorf("the tag name does not go to the tag param alone:\n%s", out)
		}
	})
}

// A GitLab schedule keeps its cron in the project settings, so no target file holds a time that no schedule named.
func TestGitLabScheduleInventsNoCron(t *testing.T) {
	cronKeys := []string{"cron:", "schedule:", "CronWorkflow", "CronJob"}
	for _, to := range []string{"gha", "gitea", "forgejo", "woodpecker", "drone", "tekton", "argo", "concourse"} {
		t.Run(to, func(t *testing.T) {
			out, _ := convertText(t, "gitlab", to, ".gitlab-ci.yml", sourcesGitLab)
			for _, k := range cronKeys {
				if strings.Contains(out, k) {
					t.Errorf("%s holds %q for a schedule that names no cron:\n%s", to, k, out)
				}
			}
		})
	}
}
