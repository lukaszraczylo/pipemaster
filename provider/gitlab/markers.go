package gitlab

import (
	"bytes"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	stepSep       = "/step/"
	continueSufx  = " || true"
	markedJobPref = "pipeline/" + pipelineID + "/job/"
)

// collapseCtx is what a collapser works on: the pipeline, the marked IR path and the argument lines after the marker.
type collapseCtx struct {
	pl   *ir.Pipeline
	path string
	args []string
}

// collapser undoes one emulation: it edits the pipeline and reports whether the marker's construct was found.
// The GitLab emitter adds an entry here for every emulation whose output the parser can recognise.
type collapser func(c *collapseCtx) bool

var collapsers = map[caps.Feature]collapser{
	caps.JobOutputs:          collapseOutputs,
	caps.JobMatrix:           collapseMatrix,
	caps.PipelineConcurrency: collapseConcurrency,
	caps.PipelineInputs:      collapseInputs,
}

// stepFeatures are the emulations that rewrite one step's lines; they collapse together because they nest.
var stepFeatures = []caps.Feature{caps.StepIf, caps.StepWorkdir, caps.StepEnv, caps.StepContinueOnError}

// markerArgs collects the "# pipemaster:arg" lines that follow each marker.
func markerArgs(src []byte) map[ir.FeatureUse][]string {
	out := map[ir.FeatureUse][]string{}
	var last []ir.FeatureUse
	for _, raw := range bytes.Split(src, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if rest, ok := strings.CutPrefix(line, argPrefix); ok && len(last) == 1 {
			out[last[0]] = append(out[last[0]], rest)
			continue
		}
		last = emitkit.ParseMarkers(raw)
	}
	return out
}

// collapseMarkers restores the IR feature behind each emulation marker; a marker it cannot restore is reported.
func (s *state) collapseMarkers(pl *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) {
	stepDone := map[string]map[caps.Feature]bool{}
	for _, u := range uses {
		if isStepFeature(u.Feature) {
			if _, ok := stepDone[u.IRPath]; !ok {
				stepDone[u.IRPath] = collapseStep(pl, u.IRPath, uses, args)
			}
			if stepDone[u.IRPath][u.Feature] {
				continue
			}
		} else if c, ok := collapsers[u.Feature]; ok && c(&collapseCtx{pl: pl, path: u.IRPath, args: args[u]}) {
			continue
		}
		s.rep.Add(report.Entry{
			IRPath: u.IRPath, Feature: u.Feature, Level: caps.Emulated, Strategy: "kept as shell text",
			Detail: "marker not collapsed: no parser rule restores " + string(u.Feature) + " at this path",
		})
	}
}

func isStepFeature(f caps.Feature) bool {
	for _, s := range stepFeatures {
		if s == f {
			return true
		}
	}
	return false
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
	if j := findJob(pl, jobID); j != nil {
		for _, st := range j.Steps {
			if st.ID == stepID {
				return st
			}
		}
	}
	return nil
}

func findJob(pl *ir.Pipeline, id string) *ir.Job {
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func findJobAt(pl *ir.Pipeline, path string) *ir.Job {
	id, ok := strings.CutPrefix(path, markedJobPref)
	if !ok {
		return nil
	}
	return findJob(pl, id)
}
