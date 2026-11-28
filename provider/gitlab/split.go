package gitlab

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

var (
	foldPipelineRE = regexp.MustCompile(`^pipeline/([^/]+)$`)
	foldJobRE      = regexp.MustCompile(`^pipeline/([^/]+)/job/(.+)$`)
)

// splitPipelines undoes the folding of several pipelines into one file. It reads the multi-pipeline markers
// and returns one pipeline per marked pipeline, with the original job IDs, triggers and conditions; jobs
// no marker claims stay in the base pipeline. Without markers the base pipeline is returned as it is.
func splitPipelines(base *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) []*ir.Pipeline {
	var order []string
	pipelineArgs := map[string]foldPipelineArg{}
	jobArgs := map[string][]foldJobEntry{}
	for _, u := range uses {
		if u.Feature != caps.ProjectMultiPipeline {
			continue
		}
		if m := foldPipelineRE.FindStringSubmatch(u.IRPath); m != nil {
			var a foldPipelineArg
			if len(args[u]) == 1 && json.Unmarshal([]byte(args[u][0]), &a) == nil {
				order = append(order, m[1])
				pipelineArgs[m[1]] = a
			}
		} else if m := foldJobRE.FindStringSubmatch(u.IRPath); m != nil {
			var a foldJobArg
			if len(args[u]) == 1 && json.Unmarshal([]byte(args[u][0]), &a) == nil {
				jobArgs[m[1]] = append(jobArgs[m[1]], foldJobEntry{id: m[2], arg: a})
			}
		}
	}
	if len(order) == 0 {
		return []*ir.Pipeline{base}
	}
	byKey := map[string]*ir.Job{}
	for _, j := range allJobs(base) {
		byKey[j.ID] = j
	}
	claimed := map[*ir.Job]bool{}
	var out []*ir.Pipeline
	for _, pid := range order {
		pa := pipelineArgs[pid]
		pl := &ir.Pipeline{ID: pid, Name: pa.Name, Triggers: pa.Triggers, Concurrency: pa.Concurrency}
		for _, je := range jobArgs[pid] {
			j := byKey[pid+foldSep+je.id]
			if j == nil || claimed[j] {
				continue
			}
			claimed[j] = true
			restoreFoldedJob(j, pid, je)
			if je.arg.Finally {
				pl.Finally = append(pl.Finally, j)
			} else {
				pl.Jobs = append(pl.Jobs, j)
			}
		}
		restoreFoldedEnv(pl, pa.EnvNames)
		restoreFoldedInputs(pl, pa.Inputs)
		if pa.Concurrency != nil {
			for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
				dropExt(&j.Ext, "resource_group")
			}
		}
		out = append(out, pl)
	}
	var rest []*ir.Job
	for _, j := range base.Jobs {
		if !claimed[j] {
			rest = append(rest, j)
		}
	}
	base.Jobs = rest
	var restFin []*ir.Job
	for _, j := range base.Finally {
		if !claimed[j] {
			restFin = append(restFin, j)
		}
	}
	base.Finally = restFin
	if len(base.Jobs)+len(base.Finally) > 0 {
		out = append(out, base)
	}
	return out
}

type foldJobEntry struct {
	id  string
	arg foldJobArg
}

// restoreFoldedJob gives a folded job its ID, needs, condition and Ext back. The stage names and the
// derived-needs flag are artefacts of the shared file, so they go.
func restoreFoldedJob(j *ir.Job, pid string, je foldJobEntry) {
	j.ID = je.id
	for i := range j.Needs {
		j.Needs[i].JobID = strings.TrimPrefix(j.Needs[i].JobID, pid+foldSep)
	}
	if je.arg.Finally {
		j.Needs = je.arg.Needs
	}
	dropExt(&j.Ext, "stage")
	dropExt(&j.Ext, ExtDerivedNeeds)
	switch {
	case je.arg.If != "":
		if x, err := expr.ParseCanonical(je.arg.If); err == nil {
			j.If = x
		}
		dropExt(&j.Ext, "rules")
	case !je.arg.HadIf:
		j.If = nil
		dropExt(&j.Ext, "rules")
	}
}

// restoreFoldedEnv lifts the variables a pipeline shared with its jobs back to pipeline level.
func restoreFoldedEnv(pl *ir.Pipeline, names []string) {
	for _, name := range names {
		for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
			if v, ok := varNamed(j.Env, name); ok {
				pl.Env = append(pl.Env, v)
				break
			}
		}
	}
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		for _, name := range names {
			j.Env = dropVar(j.Env, name)
		}
	}
}

// restoreFoldedInputs gives a pipeline its inputs back and removes the variables that carried them.
func restoreFoldedInputs(pl *ir.Pipeline, inputs []ir.Input) {
	pl.Inputs = append(pl.Inputs, inputs...)
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		for _, in := range inputs {
			j.Env = dropVar(j.Env, inputVar(in.Name))
		}
	}
}

func varNamed(vs []ir.Var, name string) (ir.Var, bool) {
	for _, v := range vs {
		if v.Name == name {
			return v, true
		}
	}
	return ir.Var{}, false
}
