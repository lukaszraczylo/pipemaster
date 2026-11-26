// Package ir is the canonical pipeline representation every provider parser and emitter converts through.
package ir

import (
	"time"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Ext keeps keys no IR field models, keyed by provider ID, so the trip back loses nothing.
type Ext map[string]any

// Project is a repo's whole CI definition; some providers split it across files.
type Project struct {
	Origin    Origin      `yaml:"origin,omitempty"`
	Pipelines []*Pipeline `yaml:"pipelines,omitempty"`
}

// Origin records where a project was parsed from.
type Origin struct {
	// Guarded is set by the GHA parser, non-nil even when empty: Provider then names the dialect of restored
	// conditions, not of every job, and the map holds the IR paths of the jobs whose deletion guard it stripped.
	Guarded  map[string]bool `yaml:"-"`
	Provider string          `yaml:"provider,omitempty"`
	Dialect  string          `yaml:"dialect,omitempty"`
	Files    []string        `yaml:"files,omitempty"`
}

// Pipeline is one workflow, pipeline or CI file.
type Pipeline struct {
	Concurrency *Concurrency `yaml:"concurrency,omitempty"`
	Permissions *Permissions `yaml:"permissions,omitempty"`
	Ext         Ext          `yaml:"ext,omitempty"`
	ID          string       `yaml:"id"`
	Name        string       `yaml:"name,omitempty"`
	Triggers    []Trigger    `yaml:"triggers,omitempty"`
	Env         []Var        `yaml:"env,omitempty"`
	Inputs      []Input      `yaml:"inputs,omitempty"`
	Jobs        []*Job       `yaml:"jobs,omitempty"`
	Finally     []*Job       `yaml:"finally,omitempty"`
}

// Job is a DAG node; Needs gives the edges.
type Job struct {
	If           expr.Expr    `yaml:"-"`
	Timeout      *Duration    `yaml:"timeout,omitempty"`
	Container    *Image       `yaml:"container,omitempty"`
	Matrix       *Matrix      `yaml:"matrix,omitempty"`
	Retry        *Retry       `yaml:"retry,omitempty"`
	Environment  *Environment `yaml:"environment,omitempty"`
	Child        *Child       `yaml:"child,omitempty"`
	Ext          Ext          `yaml:"ext,omitempty"`
	ID           string       `yaml:"id"`
	Name         string       `yaml:"name,omitempty"`
	Runner       Runner       `yaml:"runner,omitempty"`
	Needs        []Need       `yaml:"needs,omitempty"`
	Services     []Service    `yaml:"services,omitempty"`
	Steps        []*Step      `yaml:"steps,omitempty"`
	Env          []Var        `yaml:"env,omitempty"`
	Cache        []Cache      `yaml:"cache,omitempty"`
	Artifacts    []Artifact   `yaml:"artifacts,omitempty"`
	Outputs      []Output     `yaml:"outputs,omitempty"`
	AllowFailure bool         `yaml:"allow_failure,omitempty"`
	Manual       bool         `yaml:"manual,omitempty"`
}

// Child is the same-repository pipeline that a call job starts: a GitLab child pipeline or a local GHA reusable
// workflow. The pipeline never stands alone in Project.Pipelines, so no emitter writes it as an independent one.
type Child struct {
	// Gate is the condition under which the child pipeline starts at all, such as its workflow:rules. Nil admits every start.
	Gate     expr.Expr `yaml:"-"`
	Pipeline *Pipeline `yaml:"pipeline"`
	// Native lists the providers that write the child as the construct it came from; every other target gets the
	// child's jobs inlined into the calling pipeline.
	Native []string `yaml:"native,omitempty"`
	// Files lists the input files that hold the child, in the order the source reads them.
	Files []string `yaml:"files,omitempty"`
}

// StepKind says what a step runs.
type StepKind string

// Step kinds.
const (
	StepScript       StepKind = "script"
	StepAction       StepKind = "action"
	StepPlugin       StepKind = "plugin"
	StepResource     StepKind = "resource"
	StepTask         StepKind = "task"
	StepPipelineCall StepKind = "pipeline_call"
)

// Step is one unit of work inside a job.
type Step struct {
	If              expr.Expr        `yaml:"-"`
	Timeout         *Duration        `yaml:"timeout,omitempty"`
	Image           *Image           `yaml:"image,omitempty"`
	Action          *ActionRef       `yaml:"action,omitempty"`
	With            map[string]Value `yaml:"with,omitempty"`
	Ext             Ext              `yaml:"ext,omitempty"`
	ID              string           `yaml:"id"`
	Name            string           `yaml:"name,omitempty"`
	Kind            StepKind         `yaml:"kind,omitempty"`
	Shell           string           `yaml:"shell,omitempty"`
	WorkDir         string           `yaml:"workdir,omitempty"`
	Script          []string         `yaml:"script,omitempty"`
	Env             []Var            `yaml:"env,omitempty"`
	ContinueOnError bool             `yaml:"continue_on_error,omitempty"`
}

// TriggerKind says what starts a pipeline.
type TriggerKind string

// Trigger kinds.
const (
	TriggerPush         TriggerKind = "push"
	TriggerPR           TriggerKind = "pr"
	TriggerTag          TriggerKind = "tag"
	TriggerSchedule     TriggerKind = "schedule"
	TriggerManual       TriggerKind = "manual"
	TriggerAPI          TriggerKind = "api"
	TriggerPipelineDone TriggerKind = "pipeline_done"
	TriggerResource     TriggerKind = "resource"
)

// Filter is an include/exclude glob list.
type Filter struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

// ResourceRef names a non-git resource that triggers a pipeline.
type ResourceRef struct {
	Name string `yaml:"name,omitempty"`
	Type string `yaml:"type,omitempty"`
}

// Trigger is one event source for a pipeline.
type Trigger struct {
	Resource *ResourceRef `yaml:"resource,omitempty"`
	Ext      Ext          `yaml:"ext,omitempty"`
	Kind     TriggerKind  `yaml:"kind,omitempty"`
	Cron     string       `yaml:"cron,omitempty"`
	Branches Filter       `yaml:"branches,omitempty"`
	Tags     Filter       `yaml:"tags,omitempty"`
	Paths    Filter       `yaml:"paths,omitempty"`
}

// Need is a DAG edge to another job.
type Need struct {
	JobID     string `yaml:"job"`
	Artifacts bool   `yaml:"artifacts,omitempty"`
	Optional  bool   `yaml:"optional,omitempty"`
}

// Runner selects where a job runs.
type Runner struct {
	Selector map[string]string `yaml:"selector,omitempty"`
	Platform string            `yaml:"platform,omitempty"`
	Labels   []string          `yaml:"labels,omitempty"`
}

// Image is a container image reference.
type Image struct {
	Ref        string   `yaml:"ref,omitempty"`
	Entrypoint []string `yaml:"entrypoint,omitempty"`
}

// Service is a sidecar container for a job.
type Service struct {
	Name  string   `yaml:"name,omitempty"`
	Image Image    `yaml:"image,omitempty"`
	Env   []Var    `yaml:"env,omitempty"`
	Ports []string `yaml:"ports,omitempty"`
}

// Axis is one matrix dimension; value order is kept.
type Axis struct {
	Name   string   `yaml:"name"`
	Values []string `yaml:"values,omitempty"`
}

// KV is one axis assignment inside a Combo.
type KV struct {
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
}

// Combo is one matrix combination, sorted by Key.
type Combo []KV

// Matrix is a job fan-out over axes.
type Matrix struct {
	FailFast    *bool   `yaml:"fail_fast,omitempty"`
	Axes        []Axis  `yaml:"axes,omitempty"`
	Include     []Combo `yaml:"include,omitempty"`
	Exclude     []Combo `yaml:"exclude,omitempty"`
	MaxParallel int     `yaml:"max_parallel,omitempty"`
}

// Duration is a time.Duration that serialises as text such as "1h30m0s".
type Duration time.Duration

// MarshalText encodes the duration in time.Duration text form.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// UnmarshalText decodes time.ParseDuration text.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// Retry is a job retry policy.
type Retry struct {
	On       []string `yaml:"on,omitempty"`
	Attempts int      `yaml:"attempts,omitempty"`
}

// Environment is a deployment target.
type Environment struct {
	Name     string `yaml:"name,omitempty"`
	URL      string `yaml:"url,omitempty"`
	Approval bool   `yaml:"approval,omitempty"`
}

// Cache is a restorable path set.
type Cache struct {
	Key    string   `yaml:"key,omitempty"`
	Policy string   `yaml:"policy,omitempty"`
	Paths  []string `yaml:"paths,omitempty"`
}

// Artifact is a file set a job produces.
type Artifact struct {
	Name   string   `yaml:"name,omitempty"`
	Expire string   `yaml:"expire,omitempty"`
	Paths  []string `yaml:"paths,omitempty"`
}

// Output is a named job result.
type Output struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value,omitempty"`
}

// Concurrency limits parallel runs of a pipeline.
type Concurrency struct {
	Group            string `yaml:"group,omitempty"`
	CancelInProgress bool   `yaml:"cancel_in_progress,omitempty"`
}

// Permissions are token scopes and OIDC access.
type Permissions struct {
	Scopes map[string]string `yaml:"scopes,omitempty"`
	OIDC   bool              `yaml:"oidc,omitempty"`
}

// Input is a pipeline parameter.
type Input struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type,omitempty"`
	Default     string `yaml:"default,omitempty"`
	Description string `yaml:"description,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
}

// ActionKind says where an action comes from.
type ActionKind string

// Action kinds.
const (
	ActionRepo   ActionKind = "repo"
	ActionDocker ActionKind = "docker"
	ActionLocal  ActionKind = "local"
)

// Gitea host prefix forms; any other Host is a base URL without a trailing slash.
const (
	HostSelf    = "self:"
	HostBuiltin = "builtin:"
)

// ActionRef identifies a reusable action; Host says where a repo action resolves and is empty when unknown.
type ActionRef struct {
	Kind  ActionKind `yaml:"kind,omitempty"`
	Host  string     `yaml:"host,omitempty"`
	Owner string     `yaml:"owner,omitempty"`
	Repo  string     `yaml:"repo,omitempty"`
	Path  string     `yaml:"path,omitempty"`
	Ref   string     `yaml:"ref,omitempty"`
	Image string     `yaml:"image,omitempty"`
}

// ValueKind says which Value field carries the data.
type ValueKind uint8

// Value kinds; the zero value is an empty literal.
const (
	ValueLit ValueKind = iota
	ValueExpr
	ValueSecret
)

// Value is a literal, an expression or a secret reference.
type Value struct {
	Expr   expr.Expr
	Secret string
	Lit    string
	Kind   ValueKind
}

// Var is a named value, used for environment variables.
type Var struct {
	Name  string `yaml:"name"`
	Value Value  `yaml:"value"`
}
