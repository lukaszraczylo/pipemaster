package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestSuspendedCronKeepsItsScheduleThroughARoundTrip(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "c.yaml": cron("  schedules: [\"0 2 * * *\"]\n  suspend: true\n  workflowSpec: {workflowTemplateRef: {name: ci}}\n")})
	files, _ := emit(t, p)
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if same, diff := ir.Equal(p, back); !same {
		t.Errorf("A->A differs:\n%s", diff)
	}
}

func TestSuspendedCronWritesItsScheduleFromExtWhenTheTriggerHoldsNone(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "c.yaml": cron("  schedules: [\"0 2 * * *\"]\n  suspend: true\n  workflowSpec: {workflowTemplateRef: {name: ci}}\n")})
	p.Pipelines[0].Triggers[0].Cron = ""
	files, _ := emit(t, p)
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	if trs := back.Pipelines[0].Triggers; len(trs) != 1 || trs[0].Cron != "0 2 * * *" {
		t.Errorf("triggers = %+v, want the schedule back", trs)
	}
}
