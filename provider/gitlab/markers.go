package gitlab

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	stepSep      = "/step/"
	continueSufx = " || true"
)

// collapseCtx is what a collapser works on: the pipeline, the marked IR path and the argument lines after the marker.
type collapseCtx struct {
	pl *ir.Pipeline
	// ignore reports that the marker did not apply because the file was edited.
	ignore func(detail string)
	// rewrite queues a line rewrite for the combined pass that runs after every marker.
	rewrite func(j *ir.Job, r lineRewrite)
	path    string
	args    []string
}

// collapser undoes one emulation: it edits the pipeline and reports whether the marker's construct was found.
// The GitLab emitter adds an entry here for every emulation whose output the parser can recognise.
type collapser func(c *collapseCtx) bool

var collapsers = map[caps.Feature]collapser{
	caps.JobOutputs:          collapseOutputs,
	caps.JobMatrix:           collapseMatrix,
	caps.PipelineConcurrency: collapseConcurrency,
	caps.PipelineInputs:      collapseInputs,
	caps.StepAction:          collapseAction,
}

// lastCollapsers run after every other marker, because they read the lines the others restore.
var lastCollapsers = map[caps.Feature]collapser{
	caps.ScriptInterpolation: collapseRefs,
}

// stepFeatures are the emulations that rewrite one step's lines; they collapse together because they nest.
var stepFeatures = []caps.Feature{caps.StepIf, caps.StepWorkdir, caps.StepEnv, caps.StepContinueOnError}

func markerArgs(src []byte) map[ir.FeatureUse][]string { return emitkit.ParseArgs(src) }

// collapseMarkers restores the IR feature behind each emulation marker; a marker it cannot restore is reported.
func (s *state) collapseMarkers(pl *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) {
	queue := map[*ir.Job][]lineRewrite{}
	s.queue = queue
	extractMoved(pl, uses, args)
	s.restoreTriggers(pl, uses, args)
	stepDone := map[string]map[caps.Feature]bool{}
	for _, u := range uses {
		if isTriggerMark(u) {
			continue
		}
		if isStepFeature(u.Feature) {
			if _, ok := stepDone[u.IRPath]; !ok {
				stepDone[u.IRPath] = collapseStep(pl, u.IRPath, uses, args)
			}
			if stepDone[u.IRPath][u.Feature] {
				continue
			}
		} else if _, last := lastCollapsers[u.Feature]; last {
			continue
		} else if c, ok := collapsers[u.Feature]; ok && c(s.ctx(pl, u, args)) {
			continue
		}
		s.rep.Add(report.Entry{
			IRPath: u.IRPath, Feature: u.Feature, Level: caps.Emulated, Strategy: "kept as shell text",
			Detail: "marker not collapsed: no parser rule restores " + string(u.Feature) + " at this path",
		})
	}
	applyRewrites(queue)
	for _, u := range uses {
		if c, ok := lastCollapsers[u.Feature]; ok && !c(s.ctx(pl, u, args)) {
			s.rep.Add(report.Entry{
				IRPath: u.IRPath, Feature: u.Feature, Level: caps.Emulated, Strategy: "kept as shell text",
				Detail: "marker not collapsed: its argument is not a list of converted texts",
			})
		}
	}
}

var markerPipelineRE = regexp.MustCompile(`^pipeline/[^/]+`)

// retargetMarkers points every marker at the one pipeline Parse builds. The emitter writes the IR pipeline ID
// into marker paths, but the parser's pipeline is always gitlab-ci, so the prefix is the only part that differs.
func retargetMarkers(uses []ir.FeatureUse, args map[ir.FeatureUse][]string, pid string) ([]ir.FeatureUse, map[ir.FeatureUse][]string) {
	var out []ir.FeatureUse
	moved := map[ir.FeatureUse][]string{}
	for _, u := range uses {
		n := u
		n.IRPath = markerPipelineRE.ReplaceAllLiteralString(u.IRPath, "pipeline/"+pid)
		if _, seen := moved[n]; seen {
			continue
		}
		out = append(out, n)
		moved[n] = []string{}
	}
	for u, a := range args {
		n := u
		n.IRPath = markerPipelineRE.ReplaceAllLiteralString(u.IRPath, "pipeline/"+pid)
		for _, line := range a {
			if !slices.Contains(moved[n], line) {
				moved[n] = append(moved[n], line)
			}
		}
	}
	return out, moved
}

// marksFor selects the markers that belong to one pipeline. The multi-pipeline markers were consumed by the split.
func marksFor(uses []ir.FeatureUse, pl *ir.Pipeline, split bool) []ir.FeatureUse {
	var out []ir.FeatureUse
	for _, u := range uses {
		if u.Feature == caps.ProjectMultiPipeline {
			continue
		}
		if split && u.IRPath != "pipeline/"+pl.ID && !strings.HasPrefix(u.IRPath, "pipeline/"+pl.ID+"/") {
			continue
		}
		out = append(out, u)
	}
	return out
}

func isStepFeature(f caps.Feature) bool {
	for _, s := range stepFeatures {
		if s == f {
			return true
		}
	}
	return false
}

// jobPrefix starts the IR path of every job of pl.
func jobPrefix(pl *ir.Pipeline) string { return "pipeline/" + pl.ID + "/job/" }

// findStep resolves an IR path of the form pipeline/<pipeline>/job/<id>/step/<id>.
func findStep(pl *ir.Pipeline, path string) *ir.Step {
	rest, ok := strings.CutPrefix(path, jobPrefix(pl))
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
	id, ok := strings.CutPrefix(path, jobPrefix(pl))
	if !ok {
		return nil
	}
	return findJob(pl, id)
}

func (s *state) ctx(pl *ir.Pipeline, u ir.FeatureUse, args map[ir.FeatureUse][]string) *collapseCtx {
	return &collapseCtx{pl: pl, path: u.IRPath, args: args[u], rewrite: func(j *ir.Job, r lineRewrite) { s.queue[j] = append(s.queue[j], r) }, ignore: func(detail string) {
		s.rep.Add(report.Entry{IRPath: u.IRPath, Feature: u.Feature, Level: caps.Lost, Strategy: "marker ignored", Detail: detail})
	}}
}
