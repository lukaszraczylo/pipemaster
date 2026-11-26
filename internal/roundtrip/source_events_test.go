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

const webJob = `job_web:
  script: [echo web]
  rules:
    - if: '$CI_PIPELINE_SOURCE == "web"'
`

// The workflow rules decide which source gets an event: an admitted source gets one, an excluded source gets none, and a
// source that the rules admit only on some ref is reported lost, so no job is disabled without a verdict.
func TestGitLabWorkflowRulesDecideSourceEvents(t *testing.T) {
	cases := []struct {
		name, workflow string
		dispatch, lost bool
	}{
		{"admits web by source", "workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"web\" || $CI_PIPELINE_SOURCE == \"push\"'\n", true, false},
		{"admits every source", "workflow:\n  rules:\n    - when: always\n", true, false},
		{"admits web by a source rule before a ref rule", "workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"web\"'\n    - if: '$CI_COMMIT_BRANCH == \"main\"'\n", true, false},
		{"excludes web", "workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"web\"'\n      when: never\n    - when: always\n", false, false},
		{"admits only the push source", "workflow:\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n", false, false},
		{"admits web on a branch only", "workflow:\n  rules:\n    - if: '$CI_COMMIT_BRANCH == \"main\"'\n", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, rep := convert(t, "gitlab", "gha", ".gitlab-ci.yml", c.workflow+webJob)
			if got := strings.Contains(out, "\n  workflow_dispatch:"); got != c.dispatch {
				t.Errorf("workflow_dispatch present = %v, want %v:\n%s", got, c.dispatch, out)
			}
			var lost bool
			for _, e := range rep.Entries {
				lost = lost || e.Level == caps.Lost && e.Feature == caps.TriggerManual && strings.Contains(e.Detail, "workflow rules")
			}
			if lost != c.lost {
				t.Errorf("source loss reported = %v, want %v:\n%+v", lost, c.lost, rep.Entries)
			}
		})
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

// A GitLab job for the push source also runs on a tag push, because GitLab gives a tag pipeline the source push
// (https://docs.gitlab.com/ci/jobs/job_rules/). The tag trigger of Tekton and Argo therefore reports the event push,
// and a job that tests the branch still gets no branch on the tag trigger.
func TestGitLabPushJobRunsOnTagPushOnTektonAndArgo(t *testing.T) {
	src := "push_job:\n  script: [echo push]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n" + branchTagGitLab
	for _, to := range []string{"tekton", "argo"} {
		t.Run(to, func(t *testing.T) {
			out, _ := convertText(t, "gitlab", to, ".gitlab-ci.yml", src)
			for _, bad := range []string{"'''tag'''", "value: tag"} {
				if strings.Contains(out, bad) {
					t.Errorf("the tag trigger reports the event tag, and the push-only job does not start on it (%q):\n%s", bad, out)
				}
			}
			if !strings.Contains(out, "ctx-event") || !strings.Contains(out, "push") {
				t.Errorf("the push-only job has no event test:\n%s", out)
			}
		})
	}
}

// No target disables or loses the push-only job: each one has an event or a condition that holds on a branch push and on
// a tag push.
func TestGitLabPushJobIsKeptOnEveryTarget(t *testing.T) {
	src := "push_job:\n  script: [echo push]\n  rules:\n    - if: '$CI_PIPELINE_SOURCE == \"push\"'\n"
	for _, to := range []string{"gha", "gitea", "forgejo", "drone", "woodpecker", "tekton", "argo", "concourse"} {
		t.Run(to, func(t *testing.T) {
			out, rep := convert(t, "gitlab", to, ".gitlab-ci.yml", src)
			if lostJobIf(rep) || strings.Contains(out, "if: false") || strings.Contains(out, "{{= false }}") || strings.Contains(out, "pipemaster-disabled") {
				t.Errorf("the push-only job is disabled:\n%s\n%+v", out, rep.Entries)
			}
		})
	}
}

// A tag-only job of a GHA source starts on a tag push on Tekton and Argo, which read the ref type: the prefix test of
// the ref and the test of the ref type hold on the same runs.
func TestGHATagOnlyJobHasATestOfTheRefTypeOnTektonAndArgo(t *testing.T) {
	src := "on: [push, pull_request]\njobs:\n  a:\n    runs-on: ubuntu-latest\n    if: startsWith(github.ref, 'refs/tags/')\n    steps:\n      - run: echo a\n  b:\n    runs-on: ubuntu-latest\n    if: github.ref_type == 'tag'\n    steps:\n      - run: echo b\n"
	for to, want := range map[string]string{"tekton": "$(params.ctx-ref-type)", "argo": "workflow.parameters[''ctx-ref-type''] == ''tag''"} {
		t.Run(to, func(t *testing.T) {
			out, rep := convert(t, "gha", to, ".github/workflows/ci.yml", src)
			if lostJobIf(rep) {
				t.Errorf("a tag-only job is lost:\n%+v", rep.Entries)
			}
			if got := strings.Count(out, want); got < 2 {
				t.Errorf("%q occurs %d times, want a test for each of the two jobs:\n%s", want, got, out)
			}
		})
	}
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
