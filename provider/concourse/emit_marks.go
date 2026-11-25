package concourse

import "github.com/lukaszraczylo/pipemaster/ir"

// Marker arguments. Each marker is the emitkit marker line for its feature and IR path, followed by one JSON
// argument line. A parser applies an argument only when the file still holds the text it was written for; an
// edited file keeps what it says and the report names the ignored marker.

// nameArg carries the IR name and ID of a pipeline, job or step whose written name differs.
type nameArg struct {
	Name    string `json:"name,omitempty"`
	ID      string `json:"id,omitempty"`
	Written string `json:"written,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// scheduleArg carries the cron line of a time resource and the source it was written with.
type scheduleArg struct {
	Cron     string `json:"cron"`
	Resource string `json:"resource"`
	Source   string `json:"source"`
}

// matrixArg carries the matrix and the canonical text of the across that stands for it.
type matrixArg struct {
	Matrix string `json:"matrix"`
	Across string `json:"across"`
}

// gateArg carries the manual flag and the environment of a job that has no triggering get.
type gateArg struct {
	Environment *ir.Environment `json:"environment,omitempty"`
	Manual      bool            `json:"manual"`
	// Held is true when the file has no triggering get for the job.
	Held bool `json:"held"`
	// Cond is true when the job is held for a condition that only the carry of the job holds.
	Cond bool `json:"cond,omitempty"`
}

// secretArg lists the task params whose kind differs from what their name says: Secrets are secrets that look
// like plain vars, Vars are vars that look like secrets.
type secretArg struct {
	Secrets []string `json:"secrets,omitempty"`
	Vars    []string `json:"vars,omitempty"`
}

// placeholderArg carries the canonical text of the job that keeps a pipeline with no jobs valid.
type placeholderArg struct {
	Hold string `json:"hold"`
}

// actionArg carries an action step that the file holds as a placeholder task, or as the source get for a checkout.
type actionArg struct {
	// Step is the IR step as YAML text.
	Step string `json:"step"`
	// Task is the name of the placeholder task that keeps the place of the step.
	Task string `json:"task,omitempty"`
	// Hold is the canonical text of the placeholder task.
	Hold string `json:"hold,omitempty"`
	// Get names the source get that a checkout step stands for; Params is the canonical text of its params.
	Get    string `json:"get,omitempty"`
	Params string `json:"params,omitempty"`
	// Image is the image that a setup action gave to the tasks after it.
	Image string `json:"image,omitempty"`
}

// workdirArg holds a relative working directory that the emitter wrote under the workspace.
type workdirArg struct {
	WorkDir string `json:"workdir"`
	Wrote   string `json:"wrote"`
}

// ifArg carries the condition of a job whose guard is in every script task.
type ifArg struct {
	If string `json:"if"`
}

// disabledArg carries the condition and the script of a step that runs exit 0 first.
type disabledArg struct {
	If     string `json:"if"`
	Script string `json:"script"`
}

// finallyArg names the job whose hooks hold the finally jobs, the tasks that stand for them, and the digest of
// the hooks, so the parser removes those tasks only when the hooks are unedited.
type finallyArg struct {
	Job    string   `json:"job"`
	Digest string   `json:"digest"`
	Tasks  []string `json:"tasks"`
}

// tagTwinSuffix ends the name of the twin of a job that follows the tag resources.
const tagTwinSuffix = "-tag"

// tagTwinArg marks a job as the tag twin of the job named Of.
type tagTwinArg struct {
	Of string `json:"twin_of"`
}
