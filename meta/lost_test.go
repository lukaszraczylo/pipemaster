package meta_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
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

func TestKeepingEditsRestoresOnlyFieldsThatStillReadAsEmitted(t *testing.T) {
	src := lostProject()
	src.Pipelines[0].Jobs[0].Timeout = nil
	lost, _ := meta.CollectLost(src, []report.Entry{{IRPath: "pipeline/ci/job/build", Feature: caps.JobAllowFailure, Level: caps.Lost}})
	// The target file dropped allow_failure, so the parsed job reads false.
	parsed := emptied()
	m := &meta.Meta{Lost: lost, Emitted: meta.CollectEmitted(parsed, lost)}

	unedited := emptied()
	if _, kept := meta.ReattachKeepingEdits(unedited, m); len(kept) != 0 || !unedited.Pipelines[0].Jobs[0].AllowFailure {
		t.Errorf("an unedited field must be restored: kept=%v job=%+v", kept, unedited.Pipelines[0].Jobs[0])
	}

	// The user set a value the emitter never wrote: stored metadata must not replace it.
	m.Lost["pipeline/ci/job/build"].Job.Timeout = nil
	edited := emptied()
	edited.Pipelines[0].Jobs[0].AllowFailure = false
	m.Emitted["pipeline/ci/job/build"].Job.AllowFailure = true
	if _, kept := meta.ReattachKeepingEdits(edited, m); len(kept) != 1 || edited.Pipelines[0].Jobs[0].AllowFailure {
		t.Errorf("an edited field must be kept: kept=%v job=%+v", kept, edited.Pipelines[0].Jobs[0])
	}
}

func TestRestoreClearsDisabledStepCondition(t *testing.T) {
	step := func(cond expr.Expr) *ir.Step {
		return &ir.Step{ID: "s", Kind: ir.StepAction, If: cond, Action: &ir.ActionRef{Kind: ir.ActionRepo, Host: ir.HostSelf, Owner: "o", Repo: "r", Ref: "v1"}}
	}
	disabled := expr.Lit{Kind: expr.LitBool, Bool: false}
	for name, tc := range map[string]struct {
		srcIf expr.Expr
		want  expr.Expr
	}{
		"no source condition":  {nil, nil},
		"source condition set": {expr.Lit{Kind: expr.LitBool, Bool: true}, expr.Lit{Kind: expr.LitBool, Bool: true}},
	} {
		t.Run(name, func(t *testing.T) {
			src := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", Steps: []*ir.Step{step(tc.srcIf)}}}}}}
			// The emitter reports the replaced condition lost whether the source had one or not.
			entries := []report.Entry{
				{IRPath: "pipeline/ci/job/b/step/s", Feature: caps.StepAction, Level: caps.Lost},
				{IRPath: "pipeline/ci/job/b/step/s", Feature: caps.StepIf, Level: caps.Lost},
			}
			lost, _ := meta.CollectLost(src, entries)
			parsed := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", Steps: []*ir.Step{
				{ID: "s", Kind: ir.StepScript, If: disabled, Script: []string{"true"}}}}}}}}
			meta.Reattach(parsed, &meta.Meta{Lost: lost})
			if got := parsed.Pipelines[0].Jobs[0].Steps[0].If; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("If = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRestoreClearsDisabledCallJobCondition(t *testing.T) {
	disabled := expr.Lit{Kind: expr.LitBool}
	call := func(jobIf, stepIf expr.Expr) *ir.Project {
		return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", If: jobIf, Steps: []*ir.Step{
			{ID: "c", Kind: ir.StepPipelineCall, If: stepIf, Action: &ir.ActionRef{Kind: ir.ActionRepo, Host: ir.HostSelf, Owner: "o", Repo: "r", Ref: "v1"}}}}}}}}
	}
	cond := expr.Lit{Kind: expr.LitBool, Bool: true}
	for name, tc := range map[string]struct {
		jobIf, stepIf, want expr.Expr
	}{
		"no condition":       {nil, nil, nil},
		"call step has one":  {nil, cond, nil},
		"job condition kept": {cond, nil, cond},
	} {
		t.Run(name, func(t *testing.T) {
			src := call(tc.jobIf, tc.stepIf)
			entries := []report.Entry{
				{IRPath: "pipeline/ci/job/b/step/c", Feature: caps.StepAction, Level: caps.Lost},
				{IRPath: "pipeline/ci/job/b", Feature: caps.JobIf, Level: caps.Lost},
			}
			if tc.stepIf != nil {
				entries = append(entries, report.Entry{IRPath: "pipeline/ci/job/b/step/c", Feature: caps.StepIf, Level: caps.Lost})
			}
			lost, _ := meta.CollectLost(src, entries)
			parsed := call(disabled, nil)
			parsed.Pipelines[0].Jobs[0].Steps[0].Action = nil
			meta.Reattach(parsed, &meta.Meta{Lost: lost})
			if got := parsed.Pipelines[0].Jobs[0].If; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("job If = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// A lost action whose authored if: false the target could still write stays false: only the emitter's
// own marker, a recorded absent condition, may clear it.
func TestRestoreKeepsAuthoredFalseOnLostAction(t *testing.T) {
	off := expr.Lit{Kind: expr.LitBool}
	src := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", If: off, Steps: []*ir.Step{
		{ID: "c", Kind: ir.StepAction, If: off, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "o", Repo: "r", Ref: "v1"}}}}}}}}
	lost, _ := meta.CollectLost(src, []report.Entry{{IRPath: "pipeline/ci/job/b/step/c", Feature: caps.StepAction, Level: caps.Lost}})
	parsed := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", If: off, Steps: []*ir.Step{
		{ID: "c", Kind: ir.StepScript, If: off, Script: []string{"true"}}}}}}}}
	meta.Reattach(parsed, &meta.Meta{Lost: lost})
	j := parsed.Pipelines[0].Jobs[0]
	if !reflect.DeepEqual(j.If, off) || !reflect.DeepEqual(j.Steps[0].If, off) {
		t.Errorf("authored if: false was dropped: job=%#v step=%#v", j.If, j.Steps[0].If)
	}
}

func TestKeepingEditsKeepsConditionAddedToDisabledStep(t *testing.T) {
	off := expr.Lit{Kind: expr.LitBool}
	mk := func(cond expr.Expr) *ir.Project {
		return &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", Steps: []*ir.Step{
			{ID: "s", Kind: ir.StepAction, If: cond, Action: &ir.ActionRef{Kind: ir.ActionRepo, Host: ir.HostSelf, Owner: "o", Repo: "r"}}}}}}}}
	}
	entries := []report.Entry{
		{IRPath: "pipeline/ci/job/b/step/s", Feature: caps.StepAction, Level: caps.Lost},
		{IRPath: "pipeline/ci/job/b/step/s", Feature: caps.StepIf, Level: caps.Lost},
	}
	lost, _ := meta.CollectLost(mk(nil), entries)
	m := &meta.Meta{Lost: lost, Emitted: meta.CollectEmitted(mk(off), lost)}
	for name, tc := range map[string]struct{ parsed, want expr.Expr }{
		"still as emitted": {off, nil},
		"edited by user":   {expr.Lit{Kind: expr.LitBool, Bool: true}, expr.Lit{Kind: expr.LitBool, Bool: true}},
	} {
		t.Run(name, func(t *testing.T) {
			p := mk(tc.parsed)
			meta.ReattachKeepingEdits(p, m)
			if got := p.Pipelines[0].Jobs[0].Steps[0].If; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("If = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// A lost task step comes back with no script: the target wrote a placeholder script for it.
func TestRestoreDropsThePlaceholderScriptOfALostTaskStep(t *testing.T) {
	src := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", Steps: []*ir.Step{{ID: "c", Kind: ir.StepTask}}}}}}}
	lost, _ := meta.CollectLost(src, []report.Entry{{IRPath: "pipeline/ci/job/b/step/c", Feature: caps.StepAction, Level: caps.Lost}})
	parsed := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{{ID: "b", Steps: []*ir.Step{
		{ID: "c", Kind: ir.StepScript, Script: []string{"true"}}}}}}}}
	meta.Reattach(parsed, &meta.Meta{Lost: lost})
	if got := parsed.Pipelines[0].Jobs[0].Steps[0]; got.Kind != ir.StepTask || got.Script != nil {
		t.Errorf("step = kind %q script %v, want a task with no script", got.Kind, got.Script)
	}
}
