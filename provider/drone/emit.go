package drone

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// emptyJobID names the document that holds a pipeline with no job: Drone needs a document to carry its triggers.
const emptyJobID = "empty"

// docPlan is one document to write: a job, or one cell of a matrix job.
type docPlan struct {
	owner  *planSet
	pl     *ir.Pipeline
	j      *ir.Job
	name   string
	cell   ir.Combo
	combos []ir.Combo
	index  int
	first  bool
	// finally is true for a job of the pipeline finally list.
	finally bool
	// never is true for a matrix that expands to no cell.
	never bool
	own   bool
}

func (p *docPlan) cellMap() map[string]string {
	if len(p.cell) == 0 {
		return nil
	}
	out := make(map[string]string, len(p.cell))
	for _, kv := range p.cell {
		out[kv.Key] = kv.Value
	}
	return out
}

// planSet is every document of the project, and the names of the documents of each job.
type planSet struct {
	byJob map[string][]string
	plans []*docPlan
}

func jobKey(pl *ir.Pipeline, id string) string { return pl.ID + "\x00" + id }

func (s *planSet) namesOf(pl *ir.Pipeline, id string) []string { return s.byJob[jobKey(pl, id)] }

// Emit writes one .drone.yml with one YAML document per job, or per matrix cell. Every feature use goes through
// the ledger; Finish fails the call when one was never accounted for.
func (a adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	files, rep, _, err := a.emit(p)
	return files, rep, err
}

func (a adapter) emit(p *ir.Project) (map[string][]byte, *report.Report, map[string]string, error) {
	origin := ""
	if p != nil {
		origin = p.Origin.Provider
	}
	e := &emitter{led: emitkit.NewLedger("ir", dialect, a.Caps()), stepIDs: map[string]string{}}
	if lang, ok := expr.LangOf(origin); ok && origin != dialect {
		e.sem = lang.Sem
	}
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
	pls := livePipelines(p)
	plans := newPlanSet(pls, origin == dialect)
	data, err := e.project(plans, pls)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	rep, err := e.led.Finish(uses)
	if err != nil {
		return nil, rep, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	files := map[string][]byte{}
	if len(data) > 0 {
		files[fileYML] = data
	}
	return files, rep, e.stepIDs, nil
}

func livePipelines(p *ir.Project) []*ir.Pipeline {
	if p == nil {
		return nil
	}
	var out []*ir.Pipeline
	for _, pl := range p.Pipelines {
		if pl != nil {
			out = append(out, pl)
		}
	}
	return out
}

func allJobs(pl *ir.Pipeline) (jobs []*ir.Job, finally []*ir.Job) {
	for _, j := range pl.Jobs {
		if j != nil {
			jobs = append(jobs, j)
		}
	}
	for _, j := range pl.Finally {
		if j != nil {
			finally = append(finally, j)
		}
	}
	if len(jobs)+len(finally) == 0 {
		jobs = []*ir.Job{{ID: emptyJobID}}
	}
	return jobs, finally
}

// newPlanSet lists the documents of the pipelines and gives each a unique, valid name. A matrix job gets one
// document per cell, named <job>-<value>-<value> in key order.
func newPlanSet(pls []*ir.Pipeline, own bool) *planSet {
	s := &planSet{byJob: map[string][]string{}}
	var bases []string
	for _, pl := range pls {
		jobs, finally := allJobs(pl)
		for fin, js := range [][]*ir.Job{jobs, finally} {
			for _, j := range js {
				base := j.ID
				if len(pls) > 1 {
					base = pl.ID + "-" + j.ID
				}
				combos := ir.Expand(j.Matrix)
				if j.Matrix == nil || len(combos) == 0 {
					s.plans = append(s.plans, &docPlan{owner: s, pl: pl, j: j, combos: combos, first: true, finally: fin == 1, own: own, never: j.Matrix != nil})
					bases = append(bases, base)
					continue
				}
				for i, c := range combos {
					vals := make([]string, len(c))
					for k, kv := range c {
						vals[k] = kv.Value
					}
					s.plans = append(s.plans, &docPlan{owner: s, pl: pl, j: j, cell: c, combos: combos, index: i, first: i == 0, finally: fin == 1, own: own})
					bases = append(bases, strings.Join(append([]string{base}, vals...), "-"))
				}
			}
		}
	}
	for i, n := range uniqueNames(bases) {
		pln := s.plans[i]
		pln.name = n
		k := jobKey(pln.pl, pln.j.ID)
		s.byJob[k] = append(s.byJob[k], n)
	}
	return s
}

func (e *emitter) project(s *planSet, pls []*ir.Pipeline) ([]byte, error) {
	var roots []*yaml.Node
	var heads [][]string
	for _, pl := range pls {
		pp := "pipeline/" + pl.ID
		e.foreignExt(pp, pl.Ext)
		for _, k := range sortedKeys(extOf(pl.Ext)) {
			if k != ExtResources {
				e.lose(pp, caps.ExtPassthrough, "key "+k+" has no Drone form")
			}
		}
		e.loseName(pp, pl.Name)
	}
	jobIdx := map[*ir.Pipeline]int{}
	for _, pln := range s.plans {
		c := &docCtx{pl: pln.pl, j: pln.j, plan: pln, uploads: map[string]bool{}, name: pln.name, jp: "pipeline/" + pln.pl.ID + "/job/" + pln.j.ID}
		e.head = nil
		if len(pls) > 1 && pln.first {
			ji := jobIdx[pln.pl]
			jobIdx[pln.pl]++
			if err := e.markArg(caps.ProjectMultiPipeline, "pipeline/"+pln.pl.ID, multiArg{Pipeline: pln.pl.ID, Job: pln.j.ID, P: indexOf(pls, pln.pl), J: ji}); err != nil {
				return nil, err
			}
		}
		root, err := e.document(c)
		if err != nil {
			return nil, fmt.Errorf("job %s: %w", pln.j.ID, err)
		}
		roots, heads = append(roots, root), append(heads, e.head)
	}
	for _, pl := range pls {
		res, _ := extOf(pl.Ext)[ExtResources].([]any)
		for _, r := range res {
			n, err := plainNode(r)
			if err != nil {
				return nil, err
			}
			roots, heads = append(roots, n), append(heads, nil)
		}
	}
	return marshalDocs(roots, heads)
}

func indexOf(pls []*ir.Pipeline, pl *ir.Pipeline) int {
	for i, p := range pls {
		if p == pl {
			return i
		}
	}
	return 0
}

// EmittedIDs lists what Emit renames: in a single-pipeline project the pipeline, which Parse always calls drone,
// and each job whose id is not a valid document name; in every project the steps whose id is not a valid name.
func (a adapter) EmittedIDs(p *ir.Project) map[string]string {
	pls := livePipelines(p)
	_, _, steps, err := a.emit(p)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for path, src := range steps {
		out[path] = src
	}
	if len(pls) != 1 {
		return out
	}
	pl := pls[0]
	if pl.ID != pipelineID {
		out[pipelinePth] = pl.ID
	}
	for _, pln := range newPlanSet(pls, false).plans {
		if pln.j.Matrix == nil && pln.name != pln.j.ID {
			out[pipelinePth+"/job/"+pln.name] = pln.j.ID
		}
	}
	return out
}
