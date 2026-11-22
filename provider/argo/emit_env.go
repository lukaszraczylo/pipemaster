package argo

import (
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// envArg lists the env names that the emitter copied from a pipeline or a job into every container.
type envArg struct {
	// Vars holds the copied variables as IR YAML, so a job with no script step still gets them back.
	Vars string `json:"vars,omitempty"`
	// Own maps a name to the paths of the steps or jobs that set it themselves.
	Own   map[string][]string `json:"own,omitempty"`
	Names []string            `json:"names"`
}

// envMarker records the copied variables of vars that carry the names.
func (e *emitter) envMarker(f caps.Feature, path string, vars []ir.Var, names []string, own map[string][]string) {
	var copied []ir.Var
	for _, v := range vars {
		if slices.Contains(names, v.Name) && !slices.ContainsFunc(copied, func(o ir.Var) bool { return o.Name == v.Name }) {
			copied = append(copied, v)
		}
	}
	body, err := carryYAML(copied)
	if err == nil {
		err = e.markArg(f, path, envArg{Names: names, Vars: body, Own: own})
	}
	if err != nil {
		e.lose(path, f, "the env marker cannot be written: "+err.Error())
	}
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

// copiedEnv lists the names of vars that every container carries: the ones that the parser reads back.
func copiedEnv(vars []ir.Var) []string {
	var out []string
	for _, v := range vars {
		if readableEnv(v) && !slices.Contains(out, v.Name) {
			out = append(out, v.Name)
		}
	}
	return out
}

// shadowOwn lists, for each name, the paths of the steps or jobs that set the name themselves. Their value wins
// over the copied one, so the parser must leave them alone.
func shadowOwn(names []string, owners map[string][]ir.Var) map[string][]string {
	own := map[string][]string{}
	for _, path := range sortedKeys(owners) {
		for _, v := range owners[path] {
			if slices.Contains(names, v.Name) && !slices.Contains(own[v.Name], path) {
				own[v.Name] = append(own[v.Name], path)
			}
		}
	}
	return own
}

// markJobEnv records which env names of the job went into every container.
func (e *emitter) markJobEnv(j *ir.Job, path string) {
	names := copiedEnv(j.Env)
	if len(names) == 0 {
		return
	}
	owners := map[string][]ir.Var{}
	for _, st := range j.Steps {
		if st != nil {
			owners[path+"/step/"+st.ID] = st.Env
		}
	}
	e.envMarker(caps.JobEnv, path, j.Env, names, shadowOwn(names, owners))
}

// markPipelineEnv records which env names of the pipeline went into every container of every job.
func (e *emitter) markPipelineEnv(pl *ir.Pipeline, path string) {
	names := copiedEnv(pl.Env)
	if len(names) == 0 {
		return
	}
	owners := map[string][]ir.Var{}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		jp := path + "/job/" + j.ID
		owners[jp] = j.Env
		for _, st := range j.Steps {
			if st != nil {
				owners[jp+"/step/"+st.ID] = st.Env
			}
		}
	}
	e.envMarker(caps.PipelineEnv, path, pl.Env, names, shadowOwn(names, owners))
}
