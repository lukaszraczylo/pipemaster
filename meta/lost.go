package meta

import (
	"reflect"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Lost holds the source value of every field group the emit report called lost or external at one node.
// Only the fields of those features are set; the rest of each node stays empty.
type Lost struct {
	Pipeline *ir.Pipeline `yaml:"pipeline,omitempty"`
	Job      *ir.Job      `yaml:"job,omitempty"`
	Step     *ir.Step     `yaml:"step,omitempty"`
}

const triggerSeg = "/trigger/"

// CollectLost records the source fields of each feature entries mark lost or external. Entry paths must be
// source IR paths. A lost trigger records the pipeline's whole trigger list, because indexes shift in the target.
func CollectLost(p *ir.Project, entries []report.Entry) map[string]Lost {
	idx := map[string]ir.NodeRef{}
	for _, n := range ir.Nodes(p) {
		idx[n.Path] = n
	}
	out := map[string]Lost{}
	for _, e := range entries {
		if e.Level < caps.External {
			continue
		}
		path := e.IRPath
		if i := strings.Index(path, triggerSeg); i >= 0 {
			path = path[:i]
		}
		n, ok := idx[path]
		if !ok {
			continue
		}
		l := out[path]
		switch {
		case n.Pipeline != nil:
			l.Pipeline = lostPipeline(l.Pipeline, n.Pipeline, e.Feature)
		case n.Job != nil:
			l.Job = lostJob(l.Job, n.Job, e.Feature)
		case n.Step != nil:
			l.Step = lostStep(l.Step, n.Step, e.Feature)
		}
		if l != (Lost{}) {
			out[path] = l
		}
	}
	return out
}

func isTrigger(f caps.Feature) bool { return strings.HasPrefix(string(f), "trigger.") }

func lostPipeline(dst, src *ir.Pipeline, f caps.Feature) *ir.Pipeline {
	if dst == nil {
		dst = &ir.Pipeline{ID: src.ID}
	}
	switch {
	case isTrigger(f):
		dst.Triggers = src.Triggers
	case f == caps.PipelineEnv:
		dst.Env = src.Env
	case f == caps.PipelineInputs:
		dst.Inputs = src.Inputs
	case f == caps.PipelineConcurrency:
		dst.Concurrency = src.Concurrency
	case f == caps.PipelinePermissions:
		dst.Permissions = src.Permissions
	case f == caps.ScriptInterpolation:
		dst.Concurrency, dst.Env = src.Concurrency, src.Env
	}
	return dst
}

func lostJob(dst, src *ir.Job, f caps.Feature) *ir.Job {
	if dst == nil {
		dst = &ir.Job{ID: src.ID}
	}
	switch f {
	case caps.JobIf, caps.ExprOpaque:
		dst.If = src.If
	case caps.JobRunner:
		dst.Runner = src.Runner
	case caps.JobContainer:
		dst.Container = src.Container
	case caps.JobServices:
		dst.Services = src.Services
	case caps.JobMatrix:
		dst.Matrix = src.Matrix
	case caps.JobTimeout:
		dst.Timeout = src.Timeout
	case caps.JobRetry:
		dst.Retry = src.Retry
	case caps.JobAllowFailure:
		dst.AllowFailure = src.AllowFailure
	case caps.JobEnvironment, caps.JobEnvironmentApproval:
		dst.Environment = src.Environment
	case caps.JobManual:
		dst.Manual = src.Manual
	case caps.JobCache:
		dst.Cache = src.Cache
	case caps.JobArtifacts:
		dst.Artifacts = src.Artifacts
	case caps.JobOutputs:
		dst.Outputs = src.Outputs
	case caps.JobEnv:
		dst.Env = src.Env
	case caps.JobNeeds, caps.JobNeedsArtifacts:
		dst.Needs = src.Needs
	}
	return dst
}

func lostStep(dst, src *ir.Step, f caps.Feature) *ir.Step {
	if dst == nil {
		dst = &ir.Step{ID: src.ID}
	}
	switch f {
	case caps.StepIf, caps.ExprOpaque:
		dst.If = src.If
	case caps.StepEnv:
		dst.Env = src.Env
	case caps.StepContinueOnError:
		dst.ContinueOnError = src.ContinueOnError
	case caps.StepTimeout:
		dst.Timeout = src.Timeout
	case caps.StepWorkdir:
		dst.WorkDir = src.WorkDir
	case caps.StepShell:
		dst.Shell = src.Shell
	case caps.StepImage:
		dst.Image = src.Image
	case caps.ScriptInterpolation:
		dst.Script = src.Script
	case caps.StepAction, caps.ExtPassthrough:
		if src.Action != nil {
			dst.Kind, dst.Action, dst.With = src.Kind, src.Action, src.With
		}
	}
	return dst
}

// applyLost writes each stored field group over the parsed node. It returns the paths that match no node.
func applyLost(p *ir.Project, m map[string]Lost) map[string]bool {
	found := map[string]bool{}
	for _, n := range ir.Nodes(p) {
		l, ok := m[n.Path]
		if !ok {
			continue
		}
		found[n.Path] = true
		switch {
		case n.Pipeline != nil && l.Pipeline != nil:
			restorePipeline(n.Pipeline, l.Pipeline)
		case n.Job != nil && l.Job != nil:
			restoreJob(n.Job, l.Job)
		case n.Step != nil && l.Step != nil:
			restoreStep(n.Step, l.Step)
		}
	}
	return found
}

func restorePipeline(dst, src *ir.Pipeline) {
	copyNonZero(dst, src)
	// Triggers decoded from metadata hold their Ext as named maps; providers read plain ones.
	for i := range dst.Triggers {
		dst.Triggers[i].Ext = copyExt(dst.Triggers[i].Ext)
	}
}
func restoreJob(dst, src *ir.Job) { copyNonZero(dst, src) }
func restoreStep(dst, src *ir.Step) {
	if src.Action != nil {
		dst.Script = nil
	}
	copyNonZero(dst, src)
}

// copyNonZero copies every set field of a sparse src over dst. The ID and Ext fields are identity and
// stay with dst; Ext has its own record.
func copyNonZero[T any](dst, src *T) {
	d, s := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	for i := 0; i < s.NumField(); i++ {
		switch s.Type().Field(i).Name {
		case "ID", "Ext":
			continue
		}
		if !s.Field(i).IsZero() {
			d.Field(i).Set(s.Field(i))
		}
	}
}
