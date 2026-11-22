package argo_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func TestReportDoesNotCallAHeldActionDropped(t *testing.T) {
	j := scriptJob("a", "echo a")
	j.Steps = append([]*ir.Step{unmappedStep("odd")}, j.Steps...)
	files, rep := emit(t, project(j))
	back, _, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	_, rep2 := emit(t, back)
	for _, r := range []*report.Report{rep, rep2} {
		if hasEntry(r, "the step is dropped") {
			t.Errorf("the report says the step is dropped although the marker keeps it: %v", r.Entries)
		}
	}
}
