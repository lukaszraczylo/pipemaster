package meta_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/report"
)

func lostProject() *ir.Project {
	return &ir.Project{Pipelines: []*ir.Pipeline{{
		ID: "ci", Name: "CI",
		Jobs: []*ir.Job{{
			ID: "build", Name: "Build", Timeout: nil, AllowFailure: true,
			Steps: []*ir.Step{{ID: "s", Name: "Compile", Kind: ir.StepScript, Script: []string{"make"}}},
		}},
	}}}
}

func emptied() *ir.Project {
	return &ir.Project{Pipelines: []*ir.Pipeline{{
		ID:   "ci",
		Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"make"}}}}},
	}}}
}

func TestLostRestoresOnlyReportedFeatures(t *testing.T) {
	src := lostProject()
	rep := []report.Entry{
		{IRPath: "pipeline/ci/job/build", Feature: caps.JobAllowFailure, Level: caps.Lost},
		{IRPath: "pipeline/ci/job/build", Feature: caps.JobRunner, Level: caps.Emulated},
	}
	lost, skipped := meta.CollectLost(src, rep)
	if len(skipped) != 0 {
		t.Fatalf("unrecorded = %+v", skipped)
	}
	m := &meta.Meta{Lost: lost, Residual: meta.CollectResidual(src)}
	if _, ok := m.Lost["pipeline/ci/job/build"]; !ok || len(m.Lost) != 1 {
		t.Fatalf("Lost = %+v, want only the lost entry's node", m.Lost)
	}
	dst := emptied()
	meta.Reattach(dst, m)
	j := dst.Pipelines[0].Jobs[0]
	if !j.AllowFailure {
		t.Error("allow_failure was not restored")
	}
	if dst.Pipelines[0].Name != "CI" || j.Name != "Build" || j.Steps[0].Name != "Compile" {
		t.Errorf("display names were not restored: %q %q %q", dst.Pipelines[0].Name, j.Name, j.Steps[0].Name)
	}
}

func TestResidualKeepsNamesTheTargetParsed(t *testing.T) {
	src := lostProject()
	dst := emptied()
	dst.Pipelines[0].Name = "Parsed"
	meta.Reattach(dst, &meta.Meta{Residual: meta.CollectResidual(src)})
	if dst.Pipelines[0].Name != "Parsed" {
		t.Errorf("name = %q, a parsed name must win", dst.Pipelines[0].Name)
	}
}

func TestReattachReportsOrphanedLostPaths(t *testing.T) {
	m := &meta.Meta{Lost: map[string]meta.Lost{"pipeline/ci/job/gone": {Job: &ir.Job{AllowFailure: true}}}}
	orphans := meta.Reattach(emptied(), m)
	if len(orphans) != 1 || orphans[0] != "pipeline/ci/job/gone" {
		t.Errorf("orphans = %v", orphans)
	}
}
