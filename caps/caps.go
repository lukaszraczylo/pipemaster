// Package caps is the closed feature vocabulary and verdict levels shared by
// every emitter and the loss report.
package caps

import (
	"fmt"
	"sort"
)

// Level orders verdicts by severity; a higher value is a worse outcome.
type Level uint8

// Verdict levels, best to worst.
// Verdict levels, best to worst.
const (
	Native Level = iota
	Emulated
	External
	Lost
)

var levelNames = [...]string{"native", "emulated", "external", "lost"}

func (l Level) String() string {
	if int(l) < len(levelNames) {
		return levelNames[l]
	}
	return fmt.Sprintf("level(%d)", uint8(l))
}

// ParseLevel maps a verdict name to its Level.
func ParseLevel(s string) (Level, error) {
	for i, n := range levelNames {
		if n == s {
			return Level(i), nil
		}
	}
	return 0, fmt.Errorf("unknown level %q", s)
}

// MarshalText encodes the level name.
func (l Level) MarshalText() ([]byte, error) {
	if int(l) >= len(levelNames) {
		return nil, fmt.Errorf("invalid level %d", uint8(l))
	}
	return []byte(l.String()), nil
}

// UnmarshalText decodes a level name.
func (l *Level) UnmarshalText(b []byte) error {
	v, err := ParseLevel(string(b))
	if err != nil {
		return err
	}
	*l = v
	return nil
}

// Feature names one pipeline capability a target may or may not support.
type Feature string

// Trigger features.
// Feature names, grouped by pipeline scope.
const (
	TriggerPush         Feature = "trigger.push"
	TriggerPullRequest  Feature = "trigger.pull_request"
	TriggerTag          Feature = "trigger.tag"
	TriggerSchedule     Feature = "trigger.schedule"
	TriggerManual       Feature = "trigger.manual"
	TriggerAPI          Feature = "trigger.api"
	TriggerPipelineDone Feature = "trigger.pipeline_done"
	TriggerResource     Feature = "trigger.resource"
	TriggerFilterBranch Feature = "trigger.filter.branches"
	TriggerFilterTags   Feature = "trigger.filter.tags"
	TriggerFilterPaths  Feature = "trigger.filter.paths"

	PipelineEnv         Feature = "pipeline.env"
	PipelineInputs      Feature = "pipeline.inputs"
	PipelineConcurrency Feature = "pipeline.concurrency"
	PipelinePermissions Feature = "pipeline.permissions"
	PipelineFinally     Feature = "pipeline.finally"

	JobNeeds               Feature = "job.needs"
	JobNeedsArtifacts      Feature = "job.needs.artifacts"
	JobIf                  Feature = "job.if"
	JobRunner              Feature = "job.runner"
	JobContainer           Feature = "job.container"
	JobServices            Feature = "job.services"
	JobMatrix              Feature = "job.matrix"
	JobTimeout             Feature = "job.timeout"
	JobRetry               Feature = "job.retry"
	JobAllowFailure        Feature = "job.allow_failure"
	JobEnvironment         Feature = "job.environment"
	JobEnvironmentApproval Feature = "job.environment.approval"
	JobManual              Feature = "job.manual"
	JobCache               Feature = "job.cache"
	JobArtifacts           Feature = "job.artifacts"
	JobOutputs             Feature = "job.outputs"
	JobEnv                 Feature = "job.env"

	StepScript          Feature = "step.script"
	StepAction          Feature = "step.action"
	StepImage           Feature = "step.image"
	StepIf              Feature = "step.if"
	StepEnv             Feature = "step.env"
	StepContinueOnError Feature = "step.continue_on_error"
	StepTimeout         Feature = "step.timeout"
	StepWorkdir         Feature = "step.workdir"
	StepShell           Feature = "step.shell"

	ProjectMultiPipeline Feature = "project.multi_pipeline"

	ExprOpaque          Feature = "expr.opaque"
	ScriptInterpolation Feature = "script.interpolation"
	ExtPassthrough      Feature = "ext.passthrough"
	DisplayName         Feature = "display.name"
)

var allFeatures = []Feature{
	TriggerPush, TriggerPullRequest, TriggerTag, TriggerSchedule, TriggerManual,
	TriggerAPI, TriggerPipelineDone, TriggerResource, TriggerFilterBranch,
	TriggerFilterTags, TriggerFilterPaths,
	PipelineEnv, PipelineInputs, PipelineConcurrency, PipelinePermissions, PipelineFinally,
	JobNeeds, JobNeedsArtifacts, JobIf, JobRunner, JobContainer, JobServices,
	JobMatrix, JobTimeout, JobRetry, JobAllowFailure, JobEnvironment,
	JobEnvironmentApproval, JobManual, JobCache, JobArtifacts, JobOutputs, JobEnv,
	StepScript, StepAction, StepImage, StepIf, StepEnv, StepContinueOnError,
	StepTimeout, StepWorkdir, StepShell,
	ProjectMultiPipeline, ExprOpaque, ScriptInterpolation, ExtPassthrough, DisplayName,
}

// AllFeatures returns a sorted copy of the vocabulary.
func AllFeatures() []Feature {
	out := make([]Feature, len(allFeatures))
	copy(out, allFeatures)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Support is a target's verdict for one feature.
type Support struct {
	Strategy string
	Caveat   string
	Level    Level
}

// Matrix declares a target's support per feature.
type Matrix map[Feature]Support

// Lookup treats an undeclared feature as lost so omissions never pass silently.
func (m Matrix) Lookup(f Feature) Support {
	if s, ok := m[f]; ok {
		return s
	}
	return Support{Level: Lost, Caveat: "not declared by target"}
}
