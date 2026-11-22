package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestScheduleWithoutACronWritesNoCronWorkflow(t *testing.T) {
	p := project(scriptJob("a", "x"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule}}
	files, rep := emit(t, p)
	for name := range files {
		if strings.HasPrefix(name, ".argo/cron/") {
			t.Errorf("wrote %s for a schedule with no cron expression", name)
		}
	}
	for _, e := range rep.Entries {
		if e.Feature == caps.TriggerSchedule && e.Level == caps.Lost && e.IRPath == "pipeline/ci/trigger/0" {
			return
		}
	}
	t.Errorf("no lost trigger.schedule entry: %v", rep.Entries)
}
