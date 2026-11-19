package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lukaszraczylo/pipemaster/expr"
)

// Core is the JSON form of the IR subset the Lean adapter models cover: one pipeline of script jobs.
// Everything outside it is an error in ToCore, never skipped, so a vector cannot pass by dropping a field.
// Ext is the one exception: provider passthrough is not part of the subset and ToCore ignores it.

// CoreVar is a literal environment variable.
type CoreVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CoreNeed is a needs edge.
type CoreNeed struct {
	Job       string `json:"job"`
	Artifacts bool   `json:"artifacts"`
}

// CoreStep is a script step with literal lines.
type CoreStep struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Script []string  `json:"script"`
	Env    []CoreVar `json:"env"`
}

// CoreJob is a job; Retry and TimeoutMin are 0 when unset, and OnFailure marks a finally job that runs only after a failure.
type CoreJob struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Needs        []CoreNeed `json:"needs"`
	Env          []CoreVar  `json:"env"`
	Runner       []string   `json:"runner"`
	Steps        []CoreStep `json:"steps"`
	Retry        int        `json:"retry"`
	TimeoutMin   int        `json:"timeout_min"`
	Manual       bool       `json:"manual"`
	AllowFailure bool       `json:"allow_failure"`
	OnFailure    bool       `json:"on_failure"`
}

// CoreFilter is an optional branch or tag include list; a present empty list is a trigger without a filter.
type CoreFilter struct {
	Include []string `json:"include"`
	Set     bool     `json:"set"`
}

// CorePipeline is the single pipeline of a core project, with at most one trigger per kind.
type CorePipeline struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Jobs    []CoreJob  `json:"jobs"`
	Finally []CoreJob  `json:"finally"`
	Push    CoreFilter `json:"push"`
	Tag     CoreFilter `json:"tag"`
	PR      CoreFilter `json:"pr"`
}

// ErrOutsideCore means the project holds a feature the core subset does not model.
var ErrOutsideCore = errors.New("outside the core subset")

const minuteDuration = time.Minute

func outside(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrOutsideCore, fmt.Sprintf(format, a...))
}

// ToCore normalises a copy of p and returns its core JSON; it errors on anything outside the subset.
func ToCore(p *Project) ([]byte, error) {
	cp, err := CoreOf(p)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cp)
}

// CoreOf is ToCore without the JSON step.
func CoreOf(p *Project) (*CorePipeline, error) {
	if p == nil || len(p.Pipelines) != 1 || p.Pipelines[0] == nil {
		return nil, outside("want exactly one pipeline")
	}
	c := Clone(p)
	Normalize(c)
	pl := c.Pipelines[0]
	if pl.Concurrency != nil || pl.Permissions != nil || len(pl.Env) > 0 || len(pl.Inputs) > 0 {
		return nil, outside("pipeline %s holds concurrency, permissions, env or inputs", pl.ID)
	}
	out := &CorePipeline{ID: pl.ID, Name: pl.Name, Jobs: []CoreJob{}, Finally: []CoreJob{},
		Push: CoreFilter{Include: []string{}}, Tag: CoreFilter{Include: []string{}}, PR: CoreFilter{Include: []string{}}}
	for i := range pl.Triggers {
		if err := out.addTrigger(&pl.Triggers[i]); err != nil {
			return nil, err
		}
	}
	for _, j := range pl.Jobs {
		cj, err := coreJob(j, false)
		if err != nil {
			return nil, err
		}
		out.Jobs = append(out.Jobs, cj)
	}
	for _, j := range pl.Finally {
		cj, err := coreJob(j, true)
		if err != nil {
			return nil, err
		}
		out.Finally = append(out.Finally, cj)
	}
	return out, nil
}

func (c *CorePipeline) addTrigger(t *Trigger) error {
	if t.Cron != "" || t.Resource != nil || len(t.Paths.Include)+len(t.Paths.Exclude) > 0 {
		return outside("trigger %s holds cron, resource or paths", t.Kind)
	}
	var dst *CoreFilter
	var f Filter
	switch t.Kind {
	case TriggerPush:
		dst, f = &c.Push, t.Branches
		if len(t.Tags.Include)+len(t.Tags.Exclude) > 0 {
			return outside("push trigger with tags")
		}
	case TriggerTag:
		dst, f = &c.Tag, t.Tags
		if len(t.Branches.Include)+len(t.Branches.Exclude) > 0 {
			return outside("tag trigger with branches")
		}
	case TriggerPR:
		dst, f = &c.PR, t.Branches
		if len(t.Tags.Include)+len(t.Tags.Exclude) > 0 {
			return outside("pr trigger with tags")
		}
	default:
		return outside("trigger kind %q", t.Kind)
	}
	if len(f.Exclude) > 0 {
		return outside("trigger %s with excludes", t.Kind)
	}
	if dst.Set {
		return outside("two %s triggers", t.Kind)
	}
	dst.Set, dst.Include = true, append([]string{}, f.Include...)
	return nil
}

func coreVars(vs []Var) ([]CoreVar, error) {
	out := []CoreVar{}
	for _, v := range vs {
		if v.Value.Kind != ValueLit || v.Value.Expr != nil || v.Value.Secret != "" {
			return nil, outside("env %s is not a literal", v.Name)
		}
		out = append(out, CoreVar{Name: v.Name, Value: v.Value.Lit})
	}
	return out, nil
}

func coreJob(j *Job, finally bool) (CoreJob, error) {
	if j == nil {
		return CoreJob{}, outside("nil job")
	}
	if j.Container != nil || j.Matrix != nil || j.Environment != nil || len(j.Services) > 0 || len(j.Cache) > 0 ||
		len(j.Artifacts) > 0 || len(j.Outputs) > 0 || j.Runner.Platform != "" || len(j.Runner.Selector) > 0 {
		return CoreJob{}, outside("job %s holds a feature outside the subset", j.ID)
	}
	cj := CoreJob{ID: j.ID, Name: j.Name, Manual: j.Manual, AllowFailure: j.AllowFailure, Needs: []CoreNeed{}, Steps: []CoreStep{}}
	if j.If != nil {
		if c, ok := j.If.(expr.Call); !ok || !finally || c.Name != "failure" || len(c.Args) != 0 {
			return CoreJob{}, outside("job %s condition", j.ID)
		}
		cj.OnFailure = true
	}
	for _, n := range j.Needs {
		if n.Optional {
			return CoreJob{}, outside("job %s optional need", j.ID)
		}
		cj.Needs = append(cj.Needs, CoreNeed{Job: n.JobID, Artifacts: n.Artifacts})
	}
	if j.Retry != nil {
		if len(j.Retry.On) > 0 {
			return CoreJob{}, outside("job %s retry conditions", j.ID)
		}
		cj.Retry = j.Retry.Attempts
	}
	if j.Timeout != nil {
		d := time.Duration(*j.Timeout)
		if d <= 0 || d%minuteDuration != 0 {
			return CoreJob{}, outside("job %s timeout %v is not whole minutes", j.ID, d)
		}
		cj.TimeoutMin = int(d / minuteDuration)
	}
	env, err := coreVars(j.Env)
	if err != nil {
		return CoreJob{}, err
	}
	cj.Env = env
	cj.Runner = append([]string{}, j.Runner.Labels...)
	for _, s := range j.Steps {
		cs, err := coreStep(s)
		if err != nil {
			return CoreJob{}, err
		}
		cj.Steps = append(cj.Steps, cs)
	}
	return cj, nil
}

func coreStep(s *Step) (CoreStep, error) {
	if s == nil {
		return CoreStep{}, outside("nil step")
	}
	if (s.Kind != StepScript && s.Kind != "") || s.If != nil || s.Timeout != nil || s.Image != nil || s.Action != nil ||
		len(s.With) > 0 || s.Shell != "" || s.WorkDir != "" || s.ContinueOnError {
		return CoreStep{}, outside("step %s holds a feature outside the subset", s.ID)
	}
	env, err := coreVars(s.Env)
	if err != nil {
		return CoreStep{}, err
	}
	return CoreStep{ID: s.ID, Name: s.Name, Script: append([]string{}, s.Script...), Env: env}, nil
}

// FromCore builds the project for core JSON; ToCore(FromCore(b)) returns b for a normalised b.
func FromCore(b []byte) (*Project, error) {
	var c CorePipeline
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("decode core: %w", err)
	}
	return c.Project(), nil
}

// Project builds the IR project of a core pipeline.
func (c *CorePipeline) Project() *Project {
	pl := &Pipeline{ID: c.ID, Name: c.Name}
	for _, t := range []struct {
		kind TriggerKind
		f    CoreFilter
	}{{TriggerPR, c.PR}, {TriggerPush, c.Push}, {TriggerTag, c.Tag}} {
		if !t.f.Set {
			continue
		}
		tr := Trigger{Kind: t.kind}
		if t.kind == TriggerTag {
			tr.Tags = Filter{Include: nonEmpty(t.f.Include)}
		} else {
			tr.Branches = Filter{Include: nonEmpty(t.f.Include)}
		}
		pl.Triggers = append(pl.Triggers, tr)
	}
	for _, j := range c.Jobs {
		pl.Jobs = append(pl.Jobs, j.job())
	}
	for _, j := range c.Finally {
		pl.Finally = append(pl.Finally, j.job())
	}
	return &Project{Pipelines: []*Pipeline{pl}}
}

func nonEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return append([]string{}, s...)
}

func fromVars(vs []CoreVar) []Var {
	var out []Var
	for _, v := range vs {
		out = append(out, Var{Name: v.Name, Value: Value{Lit: v.Value}})
	}
	return out
}

func (c CoreJob) job() *Job {
	j := &Job{ID: c.ID, Name: c.Name, Manual: c.Manual, AllowFailure: c.AllowFailure, Env: fromVars(c.Env), Runner: Runner{Labels: nonEmpty(c.Runner)}}
	if c.OnFailure {
		j.If = expr.Call{Name: "failure"}
	}
	for _, n := range c.Needs {
		j.Needs = append(j.Needs, Need{JobID: n.Job, Artifacts: n.Artifacts})
	}
	if c.Retry > 0 {
		j.Retry = &Retry{Attempts: c.Retry}
	}
	if c.TimeoutMin > 0 {
		d := Duration(time.Duration(c.TimeoutMin) * minuteDuration)
		j.Timeout = &d
	}
	for _, s := range c.Steps {
		j.Steps = append(j.Steps, &Step{ID: s.ID, Name: s.Name, Kind: StepScript, Script: nonEmpty(s.Script), Env: fromVars(s.Env)})
	}
	return j
}
