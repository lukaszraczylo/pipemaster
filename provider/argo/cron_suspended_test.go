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

func TestSuspendedCronKeepsTheCronInExtAndNotOnTheTrigger(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"t.yaml": ciTemplate, "c.yaml": cron("  schedules: [\"0 2 * * *\"]\n  suspend: true\n  workflowSpec: {workflowTemplateRef: {name: ci}}\n")})
	tr := p.Pipelines[0].Triggers[0]
	cw, _ := ext(tr.Ext)["cronWorkflow"].(map[string]any)
	if tr.Cron != "" || cw["cron"] != "0 2 * * *" {
		t.Errorf("cron = %q, Ext = %v; want the expression in Ext only", tr.Cron, cw)
	}
}
