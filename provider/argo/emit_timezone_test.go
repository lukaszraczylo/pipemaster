package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func cronFile(t *testing.T, files map[string]string) string {
	t.Helper()
	for n, b := range files {
		if strings.HasPrefix(n, ".argo/cron/") {
			return b
		}
	}
	t.Fatalf("no CronWorkflow written:\n%s", joinFiles(files))
	return ""
}

func TestCronWorkflowRunsInUTC(t *testing.T) {
	files, _ := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"}))
	if got := cronFile(t, files); !strings.Contains(got, "timezone: UTC") {
		t.Errorf("a GitHub-style UTC cron has no timezone:\n%s", got)
	}
}

func TestCronWorkflowKeepsSourceTimezone(t *testing.T) {
	src := "apiVersion: argoproj.io/v1alpha1\nkind: CronWorkflow\nmetadata: {name: hello}\nspec:\n  schedules: ['* * * * *']\n  timezone: America/Los_Angeles\n  workflowSpec:\n    entrypoint: main\n    templates:\n    - {name: main, container: {image: busybox}}\n"
	p, _ := parseFiles(t, map[string]string{"cron.yaml": src})
	files, _ := emit(t, p)
	got := cronFile(t, files)
	if !strings.Contains(got, "timezone: America/Los_Angeles") || strings.Contains(got, "timezone: UTC") {
		t.Errorf("source timezone not kept verbatim:\n%s", got)
	}
}

func TestCronWorkflowUTCSurvivesArgoToArgo(t *testing.T) {
	files, _ := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"}))
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := emit(t, back)
	if got := cronFile(t, again); !strings.Contains(got, "timezone: UTC") {
		t.Errorf("UTC lost on re-emit:\n%s", got)
	}
}
