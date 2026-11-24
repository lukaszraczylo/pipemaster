package tekton

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// ctxOrder lists the canonical context names in the order the pipeline declares their params.
var ctxOrder = ctxparam.Names()

var (
	paramNameRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	invalidParam = regexp.MustCompile(`[^A-Za-z0-9_-]`)
	resultNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// plan is the names and shared state of one pipeline.
type plan struct {
	pl   *ir.Pipeline
	doc  *outDoc
	byID map[string]*jobPlan
	// inputs maps an IR input name to its Pipeline param name.
	inputs map[string]string
	// renamed maps a Pipeline param that an input was renamed to back to the IR input name.
	renamed map[string]string
	// ctx names the reserved ctx-* params that some task reads.
	ctx map[string]bool
	// changes holds the path plans that the generated task pipemaster-changes computes.
	changes []emitkit.ChangesPlan
	name    string
	path    string
	jobs    []*jobPlan
	// ws is true when a task mounts the shared workspace; checkout is true when one clones the repository.
	ws, checkout bool
}

// jobPlan holds the PipelineTask name and the Task name of one job.
type jobPlan struct {
	j       *ir.Job
	tname   string
	kname   string
	path    string
	finally bool
}

// ctxParams lists every reserved param name.
func ctxParams() []string {
	var out []string
	for _, c := range ctxOrder {
		p, _ := ctxparam.Param(strings.Split(c, "."))
		out = append(out, p)
	}
	return out
}

// planNames gives every pipeline, PipelineTask, Task and param a valid name that no other one holds.
func (e *emitter) planNames(pls []*ir.Pipeline) {
	pnames := map[string]bool{}
	for _, pl := range pls {
		pp := &plan{pl: pl, path: "pipeline/" + pl.ID, byID: map[string]*jobPlan{}, inputs: map[string]string{}, ctx: map[string]bool{}}
		pp.name = e.dnsName(pnames, pp.path, "pipeline id", pl.ID, maxName, true)
		e.planInputs(pp)
		tnames := map[string]bool{}
		for i, group := range [][]*ir.Job{pl.Jobs, pl.Finally} {
			for _, j := range group {
				if j == nil {
					continue
				}
				jp := &jobPlan{j: j, finally: i == 1, path: pp.path + "/job/" + j.ID}
				jp.tname = e.dnsName(tnames, jp.path, "job id", j.ID, maxName, true)
				base := pl.ID + "-" + j.ID
				if n, ok := extOf(j.Ext)["task_name"].(string); ok && e.own {
					base = n
				}
				jp.kname = e.dnsName(e.taken, jp.path, "task name", base, maxName, false)
				pp.jobs = append(pp.jobs, jp)
				pp.byID[j.ID] = jp
			}
		}
		e.plans = append(e.plans, pp)
	}
}

// planInputs names the Pipeline params. An input named like a reserved ctx-* param, or not a valid param name,
// is renamed, because the reserved param would shadow it.
func (e *emitter) planInputs(pp *plan) {
	taken := map[string]bool{}
	for _, c := range ctxParams() {
		taken[c] = true
	}
	for _, in := range pp.pl.Inputs {
		base := in.Name
		if !paramNameRE.MatchString(base) {
			base = invalidParam.ReplaceAllString(base, "_")
			if base == "" || !paramNameRE.MatchString(base) {
				base = "_" + base
			}
		}
		if taken[base] {
			base = "input-" + base
		}
		name := unique(taken, base, maxName)
		pp.inputs[in.Name] = name
		if name != in.Name {
			if pp.renamed == nil {
				pp.renamed = map[string]string{}
			}
			pp.renamed[name] = in.Name
			e.emulate(pp.path, caps.PipelineInputs, "input renamed",
				"input "+in.Name+" is written as param "+name+" because the name is reserved for context, invalid or taken")
		}
	}
}
