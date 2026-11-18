package gitlab

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	stepSep       = "/step/"
	continueSufx  = " || true"
	markedJobPref = "pipeline/" + pipelineID + "/job/"
)

// collapser undoes one emulation: it edits pl and reports whether the marker's construct was found.
// The GitLab emitter adds an entry here for every emulation whose output the parser can recognise.
type collapser func(pl *ir.Pipeline, path string) bool

var collapsers = map[caps.Feature]collapser{
	// The emitter writes a continue_on_error step as its lines each followed by "|| true", under a marker naming the step.
	caps.StepContinueOnError: collapseContinueOnError,
}

// collapseMarkers restores the IR feature behind each emulation marker; a marker it cannot restore is reported.
func (s *state) collapseMarkers(pl *ir.Pipeline, uses []ir.FeatureUse) {
	for _, u := range uses {
		if c, ok := collapsers[u.Feature]; ok && c(pl, u.IRPath) {
			continue
		}
		s.rep.Add(report.Entry{
			IRPath: u.IRPath, Feature: u.Feature, Level: caps.Emulated, Strategy: "kept as shell text",
			Detail: "marker not collapsed: no parser rule restores " + string(u.Feature) + " at this path",
		})
	}
}

// findStep resolves an IR path of the form pipeline/gitlab-ci/job/<id>/step/<id>.
func findStep(pl *ir.Pipeline, path string) *ir.Step {
	rest, ok := strings.CutPrefix(path, markedJobPref)
	if !ok {
		return nil
	}
	jobID, stepID, ok := strings.Cut(rest, stepSep)
	if !ok {
		return nil
	}
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		if j.ID != jobID {
			continue
		}
		for _, st := range j.Steps {
			if st.ID == stepID {
				return st
			}
		}
	}
	return nil
}

func collapseContinueOnError(pl *ir.Pipeline, path string) bool {
	st := findStep(pl, path)
	if st == nil || len(st.Script) == 0 {
		return false
	}
	lines := make([]string, len(st.Script))
	for i, l := range st.Script {
		trimmed, ok := strings.CutSuffix(l, continueSufx)
		if !ok {
			return false
		}
		lines[i] = trimmed
	}
	st.Script, st.ContinueOnError = lines, true
	return true
}
