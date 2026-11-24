package argo

import (
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	entryTemplate = "main"
	exitTemplate  = "exit-handler"
	inputPrefix   = "input-"
)

var (
	paramNameRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	invalidParam = regexp.MustCompile(`[^A-Za-z0-9_-]`)
	outputNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// ctxOrder lists the canonical context names in the order the pipeline declares their params.
var ctxOrder = ctxparam.Names()

// plan is the names and shared state of one pipeline.
type plan struct {
	// changes holds the path plans that the generated task pipemaster-changes computes.
	changes []emitkit.ChangesPlan
	pl      *ir.Pipeline
	doc     *outDoc
	byID    map[string]*jobPlan
	// inputs maps an IR input name to its parameter name.
	inputs map[string]string
	// renamed maps a parameter that an input was renamed to back to the IR input name.
	renamed map[string]string
	// ctx names the reserved ctx-* params that something reads.
	ctx map[string]bool
	// tmpls holds the template names in use.
	tmpls map[string]bool
	// claims are the volumeClaimTemplates that caches need, by claim name.
	claims map[string]cacheClaim
	name   string
	path   string
	jobs   []*jobPlan
	// args lists the workflow arguments in the order the template declares them.
	args []argSpec
	// taskNames holds the DAG task names in use.
	taskNames map[string]bool
	// claimOrder lists the claim names in order of first use.
	claimOrder []string
}

// argSpec is one workflow argument. A context argument has no value of its own.
type argSpec struct {
	name  string
	value string
	has   bool
	ctx   bool
}

// cacheClaim is one PersistentVolumeClaim of a job cache.
type cacheClaim struct {
	name string
	size string
}

// jobPlan holds the task name and the template name of one job.
type jobPlan struct {
	j       *ir.Job
	task    string
	tmpl    string
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

// planNames gives every pipeline, task, template and param a valid name that no other one holds.
func (e *emitter) planNames(pls []*ir.Pipeline) {
	pnames := map[string]bool{}
	for _, pl := range pls {
		pp := &plan{
			pl: pl, path: "pipeline/" + pl.ID, byID: map[string]*jobPlan{}, inputs: map[string]string{}, ctx: map[string]bool{},
			tmpls: map[string]bool{entryTemplate: true, exitTemplate: true}, claims: map[string]cacheClaim{},
		}
		pp.name = e.dnsName(pnames, pp.path, "pipeline id", pl.ID, maxName, true)
		e.planInputs(pp)
		tnames := map[string]bool{}
		pp.taskNames = tnames
		for i, group := range [][]*ir.Job{pl.Jobs, pl.Finally} {
			for _, j := range group {
				if j == nil {
					continue
				}
				jp := &jobPlan{j: j, finally: i == 1, path: pp.path + "/job/" + j.ID}
				jp.task = e.dnsName(tnames, jp.path, "job id", j.ID, maxName, true)
				jp.tmpl = unique(pp.tmpls, jp.task, maxName)
				pp.jobs = append(pp.jobs, jp)
				pp.byID[j.ID] = jp
			}
		}
		e.plans = append(e.plans, pp)
	}
}

// planInputs names the workflow params. An input named like a reserved ctx-* param, or not a valid param name,
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
			base = inputPrefix + base
		}
		name := unique(taken, base, maxName)
		pp.inputs[in.Name] = name
		if name != in.Name {
			if pp.renamed == nil {
				pp.renamed = map[string]string{}
			}
			pp.renamed[name] = in.Name
			e.emulate(pp.path, caps.PipelineInputs, "input renamed",
				"input "+in.Name+" is written as parameter "+name+" because the name is reserved for context, invalid or taken")
		}
	}
}
