package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestCronWithAnInlineWorkflowSpecRoundTrips(t *testing.T) {
	src := "apiVersion: argoproj.io/v1alpha1\nkind: CronWorkflow\nmetadata: {name: hello}\nspec:\n  schedules: ['* * * * *']\n  timezone: America/Los_Angeles\n  workflowSpec:\n    entrypoint: main\n    templates:\n    - {name: main, container: {image: busybox}}\n"
	p, _ := parseFiles(t, map[string]string{"cron.yaml": src})
	files, _ := emit(t, p)
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}
