package argo

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// envArg lists the env names that the emitter copied from a pipeline or a job into every container.
type envArg struct {
	Names []string `json:"names"`
}

// readableEnv is true for a variable that the parser reads back from a container as a step variable. A secret or
// vars reference becomes a valueFrom entry, which has no variable form.
func readableEnv(v ir.Var) bool {
	switch v.Value.Kind {
	case ir.ValueSecret:
		return false
	case ir.ValueExpr:
		r, ok := v.Value.Expr.(expr.Ref)
		return !ok || len(r.Path) != 2 || (r.Path[0] != expr.PrefixSecrets && r.Path[0] != expr.PrefixVars)
	}
	return true
}

// copiedEnv lists the names of vars that every container carries unchanged: readable, and set by no step or job in shadow.
func copiedEnv(vars []ir.Var, shadow ...[]ir.Var) []string {
	var out []string
	for _, v := range vars {
		if !readableEnv(v) || slices.Contains(out, v.Name) {
			continue
		}
		hidden := false
		for _, s := range shadow {
			hidden = hidden || slices.ContainsFunc(s, func(o ir.Var) bool { return o.Name == v.Name })
		}
		if !hidden {
			out = append(out, v.Name)
		}
	}
	return out
}

// markJobEnv records which env names of the job went into every container.
func (e *emitter) markJobEnv(j *ir.Job, path string) {
	var steps []ir.Var
	for _, st := range j.Steps {
		if st != nil {
			steps = append(steps, st.Env...)
		}
	}
	if names := copiedEnv(j.Env, steps); len(names) > 0 {
		if err := e.markArg(caps.JobEnv, path, envArg{Names: names}); err != nil {
			e.lose(path, caps.JobEnv, "the env marker cannot be written: "+err.Error())
		}
	}
}

// markPipelineEnv records which env names of the pipeline went into every container of every job.
func (e *emitter) markPipelineEnv(pl *ir.Pipeline, path string) {
	var shadow []ir.Var
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		shadow = append(shadow, j.Env...)
		for _, st := range j.Steps {
			if st != nil {
				shadow = append(shadow, st.Env...)
			}
		}
	}
	if names := copiedEnv(pl.Env, shadow); len(names) > 0 {
		if err := e.markArg(caps.PipelineEnv, path, envArg{Names: names}); err != nil {
			e.lose(path, caps.PipelineEnv, "the env marker cannot be written: "+err.Error())
		}
	}
}
