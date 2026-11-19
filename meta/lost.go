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

// Unrecorded is a lost or external feature the metadata cannot store, so it will not return on the way back.
type Unrecorded struct {
	Path    string
	Feature caps.Feature
	Reason  string
}

// kind selects the table of a node.
type kind int

const (
	kindPipeline kind = iota
	kindJob
	kindStep
)

// recordViaExt marks a feature whose data the Ext record already holds.
const recordViaExt = "Ext"

var triggerFeatureNames = []caps.Feature{
	caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag, caps.TriggerSchedule, caps.TriggerManual,
	caps.TriggerAPI, caps.TriggerPipelineDone, caps.TriggerResource, caps.TriggerFilterBranch,
	caps.TriggerFilterTags, caps.TriggerFilterPaths,
}

// recorded lists, per node kind, the struct fields that restore a feature. A feature a kind can carry but
// has no entry here must be in unrecorded, which the table test enforces.
var recorded = map[kind]map[caps.Feature][]string{
	kindPipeline: withTriggers(map[caps.Feature][]string{
		caps.PipelineEnv:         {"Env"},
		caps.PipelineInputs:      {"Inputs"},
		caps.PipelineConcurrency: {"Concurrency"},
		caps.PipelinePermissions: {"Permissions"},
		caps.ScriptInterpolation: {"Env", "Concurrency"},
		caps.ExprOpaque:          {"Env"},
		caps.DisplayName:         {"Name"},
		caps.ExtPassthrough:      {recordViaExt},
	}),
	kindJob: {
		caps.JobNeeds: {"Needs"}, caps.JobNeedsArtifacts: {"Needs"}, caps.JobIf: {"If"},
		caps.JobRunner: {"Runner"}, caps.JobContainer: {"Container"}, caps.JobServices: {"Services"},
		caps.JobMatrix: {"Matrix"}, caps.JobTimeout: {"Timeout"}, caps.JobRetry: {"Retry"},
		caps.JobAllowFailure: {"AllowFailure"}, caps.JobEnvironment: {"Environment"},
		caps.JobEnvironmentApproval: {"Environment"}, caps.JobManual: {"Manual"}, caps.JobCache: {"Cache"},
		caps.JobArtifacts: {"Artifacts"}, caps.JobOutputs: {"Outputs"}, caps.JobEnv: {"Env"},
		caps.ExprOpaque:          {"If", "Env", "Services"},
		caps.ScriptInterpolation: {"Runner", "Container", "Services", "Matrix", "Environment", "Cache", "Artifacts", "Outputs", "Env"},
		caps.DisplayName:         {"Name"},
		caps.ExtPassthrough:      {recordViaExt},
	},
	kindStep: {
		caps.StepIf: {"If"}, caps.StepEnv: {"Env"}, caps.StepContinueOnError: {"ContinueOnError"},
		caps.StepTimeout: {"Timeout"}, caps.StepWorkdir: {"WorkDir"}, caps.StepShell: {"Shell"},
		caps.StepImage: {"Image"}, caps.StepScript: {"Script"}, caps.StepAction: {"Kind", "Action", "With"},
		caps.ExprOpaque:          {"If", "Env", "With"},
		caps.ScriptInterpolation: {"Script", "WorkDir", "Env", "With"},
		caps.DisplayName:         {"Name"},
		caps.ExtPassthrough:      {"Kind", "Action", "With", recordViaExt},
	},
}

func withTriggers(m map[caps.Feature][]string) map[caps.Feature][]string {
	for _, f := range triggerFeatureNames {
		m[f] = []string{"Triggers"}
	}
	return m
}

// unrecorded lists the features a node kind can lose that the metadata does not hold, with the reason.
// Everything else a kind cannot carry at all is reported as not applicable.
var unrecorded = map[kind]map[caps.Feature]string{
	kindPipeline: {
		caps.PipelineFinally:      "the finally jobs are structure that the markers in the file rebuild",
		caps.ProjectMultiPipeline: "the pipeline split is rebuilt from the markers in the file",
	},
	kindJob:  {},
	kindStep: {},
}

// notApplicable lists the features a node kind never carries.
func notApplicable(k kind, f caps.Feature) bool {
	switch k {
	case kindPipeline:
		return strings.HasPrefix(string(f), "job.") || strings.HasPrefix(string(f), "step.")
	case kindJob:
		return strings.HasPrefix(string(f), "trigger.") || strings.HasPrefix(string(f), "pipeline.") ||
			strings.HasPrefix(string(f), "step.") || f == caps.ProjectMultiPipeline
	default:
		return strings.HasPrefix(string(f), "trigger.") || strings.HasPrefix(string(f), "pipeline.") ||
			strings.HasPrefix(string(f), "job.") || f == caps.ProjectMultiPipeline
	}
}

// CollectLost records the source fields of each feature the entries mark lost or external. Entry paths must
// be source IR paths. A lost trigger records the pipeline's whole trigger list, because indexes shift in the
// target. A feature the tables cannot store comes back as Unrecorded, never silently.
func CollectLost(p *ir.Project, entries []report.Entry) (map[string]Lost, []Unrecorded) {
	idx := map[string]ir.NodeRef{}
	for _, n := range ir.Nodes(p) {
		idx[n.Path] = n
	}
	out := map[string]Lost{}
	var skipped []Unrecorded
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
			skipped = append(skipped, Unrecorded{e.IRPath, e.Feature, "no node at this path in the source project"})
			continue
		}
		k := kindOf(n)
		fields, ok := recorded[k][e.Feature]
		if !ok {
			reason, known := unrecorded[k][e.Feature]
			if !known {
				reason = "the metadata has no record for this feature on this node"
			}
			skipped = append(skipped, Unrecorded{e.IRPath, e.Feature, reason})
			continue
		}
		if len(fields) == 1 && fields[0] == recordViaExt {
			continue
		}
		l := out[path]
		switch {
		case n.Pipeline != nil:
			l.Pipeline = copyFields(l.Pipeline, n.Pipeline, fields, e.Feature)
		case n.Job != nil:
			l.Job = copyFields(l.Job, n.Job, fields, e.Feature)
		case n.Step != nil:
			l.Step = copyFields(l.Step, n.Step, fields, e.Feature)
		}
		out[path] = l
	}
	return out, skipped
}

func kindOf(n ir.NodeRef) kind {
	switch {
	case n.Pipeline != nil:
		return kindPipeline
	case n.Job != nil:
		return kindJob
	}
	return kindStep
}

// copyFields copies the named fields of src into the sparse node dst, which it creates on first use.
// An action step records its action only when the step has one.
func copyFields[T any](dst, src *T, fields []string, f caps.Feature) *T {
	if dst == nil {
		dst = new(T)
		reflect.ValueOf(dst).Elem().FieldByName("ID").Set(reflect.ValueOf(src).Elem().FieldByName("ID"))
	}
	d, s := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	for _, name := range fields {
		if name == recordViaExt {
			continue
		}
		if a := s.FieldByName("Action"); f == caps.ExtPassthrough && a.IsValid() && a.IsNil() {
			continue
		}
		d.FieldByName(name).Set(s.FieldByName(name))
	}
	return dst
}

// applyLost writes each stored field group over the parsed node. With keepEdits a field the node already holds
// stays, and a path whose stored field differs from it is returned in kept. It returns the paths that match a node.
func applyLost(p *ir.Project, m map[string]Lost, keepEdits bool) (found, kept map[string]bool) {
	found, kept = map[string]bool{}, map[string]bool{}
	for _, n := range ir.Nodes(p) {
		l, ok := m[n.Path]
		if !ok {
			continue
		}
		found[n.Path] = true
		var differs bool
		switch {
		case n.Pipeline != nil && l.Pipeline != nil:
			differs = restorePipeline(n.Pipeline, l.Pipeline, keepEdits)
		case n.Job != nil && l.Job != nil:
			differs = restoreJob(n.Job, l.Job, keepEdits)
		case n.Step != nil && l.Step != nil:
			differs = restoreStep(n.Step, l.Step, keepEdits)
		}
		if differs {
			kept[n.Path] = true
		}
	}
	return found, kept
}

func restorePipeline(dst, src *ir.Pipeline, keepEdits bool) bool {
	differs := copyNonZero(dst, src, keepEdits)
	// Triggers decoded from metadata hold their Ext as named maps; providers read plain ones.
	for i := range dst.Triggers {
		dst.Triggers[i].Ext = copyExt(dst.Triggers[i].Ext)
	}
	return differs
}
func restoreJob(dst, src *ir.Job, keepEdits bool) bool { return copyNonZero(dst, src, keepEdits) }
func restoreStep(dst, src *ir.Step, keepEdits bool) bool {
	if src.Action != nil && !keepEdits {
		dst.Script = nil
	}
	return copyNonZero(dst, src, keepEdits)
}

// copyNonZero copies every set field of a sparse src over dst. The ID and Ext fields are identity and
// stay with dst; Ext has its own record. With keepEdits a field dst already holds stays, and the result says
// whether any such field differed from src.
func copyNonZero[T any](dst, src *T, keepEdits bool) (differs bool) {
	d, s := reflect.ValueOf(dst).Elem(), reflect.ValueOf(src).Elem()
	for i := 0; i < s.NumField(); i++ {
		switch s.Type().Field(i).Name {
		case "ID", "Ext":
			continue
		}
		if s.Field(i).IsZero() {
			continue
		}
		if keepEdits && !d.Field(i).IsZero() {
			differs = differs || !reflect.DeepEqual(d.Field(i).Interface(), s.Field(i).Interface())
			continue
		}
		d.Field(i).Set(s.Field(i))
	}
	return differs
}
