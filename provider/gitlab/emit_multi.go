package gitlab

import (
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const foldSep = ":"

// foldJobArg and foldPipelineArg are the marker arguments Parse reads to split a folded file again.
type foldJobArg struct {
	If      string    `json:"if,omitempty"`
	Needs   []ir.Need `json:"needs,omitempty"`
	HadIf   bool      `json:"hadIf,omitempty"`
	Finally bool      `json:"finally,omitempty"`
}

type foldPipelineArg struct {
	Concurrency *ir.Concurrency `json:"concurrency,omitempty"`
	Name        string          `json:"name,omitempty"`
	EnvNames    []string        `json:"env,omitempty"`
	Inputs      []ir.Input      `json:"inputs,omitempty"`
	Triggers    []ir.Trigger    `json:"triggers"`
}

type foldedJob struct {
	cond   expr.Expr
	origIf expr.Expr
	// shared names the pipeline variables the copy received, so their losses are reported on the pipeline.
	shared map[string]bool
	path   string
	pid    string
	jid    string
	parts  []foldPart
	needs  []ir.Need
	// disabled marks a pipeline whose triggers GitLab cannot express: its jobs must not run on another pipeline's trigger.
	disabled bool
	finally  bool
}

// foldPart is the condition and path filter of one trigger.
type foldPart struct {
	cond    expr.Expr
	changes []string
}

// folded is the state of several pipelines written as one: each job sits under "<pipeline>:<job>", and
// carries its pipeline's trigger condition, because GitLab has one workflow per file.
type folded struct {
	jobs  map[*ir.Job]foldedJob
	marks []string
	pls   []*ir.Pipeline
}

// pathOf is the IR path of a job as the ledger knows it, which a folded copy does not carry in its own ID.
func (e *emitter) pathOf(pid string, j *ir.Job) string {
	if e.fold != nil {
		if fj, ok := e.fold.jobs[j]; ok {
			return fj.path
		}
	}
	return jobPath(pid, j.ID)
}

// foldJob is what the fold knows about a folded copy; the zero value for any other job.
func (e *emitter) foldJob(j *ir.Job) foldedJob {
	if e.fold == nil {
		return foldedJob{}
	}
	return e.fold.jobs[j]
}

// foldJobMarkers tags a folded job with its pipeline so Parse can split the file again.
func (e *emitter) foldJobMarkers(j *ir.Job) []string {
	if e.fold == nil {
		return nil
	}
	fj := e.fold.jobs[j]
	arg := foldJobArg{Finally: fj.finally, HadIf: fj.origIf != nil}
	if fj.finally {
		arg.Needs = fj.needs
	}
	if fj.origIf != nil && !expr.HasOpaque(fj.origIf) {
		arg.If = expr.String(fj.origIf)
	}
	a, err := markerArg(arg)
	if err != nil {
		return nil
	}
	return []string{emitkit.Marker(caps.ProjectMultiPipeline, fj.path), a}
}

// triggerParts lists the condition and path filter of each trigger that has a GitLab form.
func triggerParts(pl *ir.Pipeline) []foldPart {
	var out []foldPart
	for i := range pl.Triggers {
		c, ok := triggerRule(&pl.Triggers[i])
		if !ok {
			continue
		}
		if _, err := exprgitlab.Print(c); err != nil {
			continue
		}
		out = append(out, foldPart{cond: c, changes: pl.Triggers[i].Paths.Include})
	}
	return out
}

// partsCond is the single condition for parts without path filters; ok is false when any part has one,
// because then each part needs its own rule.
func partsCond(parts []foldPart) (expr.Expr, bool) {
	var cs []expr.Expr
	for _, p := range parts {
		if len(p.changes) > 0 {
			return nil, false
		}
		cs = append(cs, p.cond)
	}
	return orAny(cs), true
}

// foldPipelines merges the pipelines into one whose job IDs are "<pipeline>:<job>". Variables, inputs and
// the concurrency group of a pipeline move into its jobs, because a GitLab file has one global set.
func (e *emitter) foldPipelines(pls []*ir.Pipeline) (*ir.Pipeline, error) {
	f := &folded{jobs: map[*ir.Job]foldedJob{}, pls: pls}
	e.fold = f
	merged := &ir.Pipeline{ID: pipelineID}
	for i, pl := range pls {
		pp := "pipeline/" + pl.ID
		if i > 0 {
			e.emulate(pp, caps.ProjectMultiPipeline, "jobs of every pipeline in one file under <pipeline>:<job> keys",
				"each job carries its pipeline's trigger condition and a marker; workflow:rules is the union of all triggers")
		}
		e.foreignExt(pp, pl.Ext)
		e.loseName(pp, pl.Name, "")
		shared, group, inputs, err := e.foldShared(pp, pl)
		if err != nil {
			return nil, err
		}
		parts := triggerParts(pl)
		cond, plain := partsCond(parts)
		if plain {
			parts = nil
		}
		disabled := (len(pl.Triggers) > 0 || e.noTriggers(pl)) && len(triggerParts(pl)) == 0
		if e.noTriggers(pl) {
			e.loseNoTriggers(pl)
		}
		for k := range pl.Triggers {
			e.triggerNotes(triggerPath(pl.ID, k), &pl.Triggers[k])
			e.foreignExt(triggerPath(pl.ID, k), pl.Triggers[k].Ext)
		}
		for _, j := range pl.Jobs {
			merged.Jobs = append(merged.Jobs, f.add(pl, j, shared, group, folded1{cond: cond, parts: parts, disabled: disabled}, false))
		}
		for _, j := range pl.Finally {
			merged.Finally = append(merged.Finally, f.add(pl, j, shared, group, folded1{cond: cond, parts: parts, disabled: disabled}, true))
		}
		arg := foldPipelineArg{Name: pl.Name, Triggers: pl.Triggers, Concurrency: pl.Concurrency}
		arg.Inputs = inputs
		for _, v := range pl.Env {
			arg.EnvNames = append(arg.EnvNames, v.Name)
		}
		a, err := markerArg(arg)
		if err != nil {
			return nil, err
		}
		f.marks = append(f.marks, emitkit.Marker(caps.ProjectMultiPipeline, pp), a)
		rm, err := refMarks(pp, pl)
		if err != nil {
			return nil, err
		}
		f.marks = append(f.marks, rm...)
	}
	return merged, nil
}

// foldShared returns the variables every job of pl receives, the resource group they share and the inputs
// that became variables.
func (e *emitter) foldShared(pp string, pl *ir.Pipeline) (vars []ir.Var, group string, inputs []ir.Input, err error) {
	vars = append(vars, pl.Env...)
	for _, in := range pl.Inputs {
		name := inputVar(in.Name)
		if name == "" || hasVar(vars, name) {
			e.lose(pp, caps.PipelineInputs, "input "+in.Name+" is not a unique variable name; the variable "+name+" is taken")
			e.dropInput(in.Name)
			continue
		}
		vars = append(vars, ir.Var{Name: name, Value: ir.Value{Lit: in.Default}})
		inputs = append(inputs, in)
		e.emulate(pp, caps.PipelineInputs, "variable copied into every job of the pipeline", "the pipeline marker keeps the input list")
	}
	if pl.Concurrency != nil {
		if group, err = e.field(pp, caps.PipelineConcurrency, pl.Concurrency.Group); err != nil {
			return nil, "", nil, err
		}
		e.emulate(pp, caps.PipelineConcurrency, "resource_group on every job of the pipeline", "queues, does not cancel")
		e.cancelLost(pp, pl.Concurrency)
	}
	return vars, group, inputs, nil
}

// folded1 is the trigger guard shared by the jobs of one pipeline.
type folded1 struct {
	cond     expr.Expr
	parts    []foldPart
	disabled bool
}

// add copies a job under its folded ID. The copy shares steps and fields with the original and never writes back.
func (f *folded) add(pl *ir.Pipeline, j *ir.Job, shared []ir.Var, group string, g folded1, finally bool) *ir.Job {
	cp := *j
	cp.ID = pl.ID + foldSep + j.ID
	cp.Needs = make([]ir.Need, len(j.Needs))
	for i, n := range j.Needs {
		cp.Needs[i] = n
		cp.Needs[i].JobID = pl.ID + foldSep + n.JobID
	}
	cp.Env = append([]ir.Var{}, j.Env...)
	received := map[string]bool{}
	for _, v := range shared {
		if !hasVar(cp.Env, v.Name) {
			cp.Env = append(cp.Env, v)
			received[v.Name] = hasVar(pl.Env, v.Name)
		}
	}
	if group != "" {
		cp.Ext = withGroup(j.Ext, group)
	}
	f.jobs[&cp] = foldedJob{shared: received, needs: j.Needs, pid: pl.ID, jid: j.ID, path: jobPath(pl.ID, j.ID), cond: g.cond, parts: g.parts, disabled: g.disabled, origIf: j.If, finally: finally}
	return &cp
}

// withGroup copies the Ext map and adds the job's resource group unless the job names one already.
func withGroup(x ir.Ext, group string) ir.Ext {
	out := ir.Ext{}
	for k, v := range x {
		out[k] = v
	}
	gl := map[string]any{}
	for k, v := range extOf(x) {
		gl[k] = v
	}
	if _, own := gl["resource_group"]; !own {
		gl["resource_group"] = group
	}
	out[dialect] = gl
	return out
}

// foldHeader writes workflow:rules as the union of every pipeline's trigger rules and returns the pipeline markers.
func (e *emitter) foldHeader(m *omap, _ *pipeCtx) ([]string, error) {
	rules := seqOf()
	seen := map[string]bool{}
	used := false
	for _, pl := range e.fold.pls {
		used = used || len(pl.Triggers) > 0 || e.noTriggers(pl)
		for i := range pl.Triggers {
			r, key := e.triggerRuleNode(pl.ID, i, &pl.Triggers[i])
			if r != nil && !seen[key] {
				seen[key] = true
				rules.Content = append(rules.Content, r)
			}
		}
	}
	if used {
		if len(rules.Content) == 0 {
			rules.Content = append(rules.Content, ruleNever())
		}
		w := newMap()
		w.set("rules", rules)
		m.set(extWorkflow, w.n)
	}
	return e.fold.marks, nil
}
