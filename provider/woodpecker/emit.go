package woodpecker

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/vars"
)

const workflowExt = ".yaml"

// Emit writes one workflow file per job under .woodpecker/. Every feature use goes through the ledger;
// Finish fails the call when one was never accounted for.
func (a adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	p, vres := vars.Prepare(p, dialect)
	files, rep, _, err := a.emit(p)
	vres.Report(rep)
	return files, rep, err
}

func (a adapter) emit(p *ir.Project) (map[string][]byte, *report.Report, map[string]string, error) {
	origin := ""
	if p != nil {
		origin = p.Origin.Provider
	}
	e := &emitter{
		origin: origin, from: sourceDialect(origin), own: origin == dialect, led: emitkit.NewLedger("ir", dialect, a.Caps()),
		stepIDs: map[string]string{},
	}
	e.gated = emitkit.GatedSteps(p)
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
	files, err := e.project(livePipelines(p))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	rep, err := e.led.Finish(uses)
	if err != nil {
		return nil, rep, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	return files, rep, e.stepIDs, nil
}

func (e *emitter) project(pls []*ir.Pipeline) (map[string][]byte, error) {
	files := map[string][]byte{}
	names, err := workflowNames(pls)
	if err != nil {
		return nil, err
	}
	for i, pl := range pls {
		pp := "pipeline/" + pl.ID
		e.foreignExt(pp, pl.Ext)
		if len(extOf(pl.Ext)) > 0 {
			e.lose(pp, caps.ExtPassthrough, "Woodpecker has no pipeline-level keys; the Ext of the pipeline is kept in round-trip metadata")
		}
		e.loseName(pp, pl.Name)
		for ji, j := range workflowJobs(pl) {
			c := &wfCtx{
				pl: pl, j: j, names: names[i], uploads: map[string]bool{}, jp: pp + "/job/" + j.ID,
				name: names[i][j.ID], finally: !containsJob(pl.Jobs, j) && len(allJobs(pl)) > 0,
			}
			if len(pls) > 1 {
				c.multi = &multiArg{Pipeline: pl.ID, Job: j.ID, P: i, J: ji}
			}
			data, err := e.workflowFile(c)
			if err != nil {
				return nil, fmt.Errorf("job %s: %w", j.ID, err)
			}
			files[dirName+"/"+c.name+workflowExt] = data
		}
	}
	return files, nil
}

func containsJob(js []*ir.Job, j *ir.Job) bool {
	for _, x := range js {
		if x == j {
			return true
		}
	}
	return false
}

// workflowFile renders one workflow with its marker and lost-text comments in the head.
func (e *emitter) workflowFile(c *wfCtx) ([]byte, error) {
	e.head = nil
	if len(allJobs(c.pl)) == 0 {
		e.mark(caps.StepScript, "pipeline/"+c.pl.ID)
	}
	if c.multi != nil {
		if err := e.markArg(caps.ProjectMultiPipeline, "pipeline/"+c.pl.ID, c.multi); err != nil {
			return nil, err
		}
	}
	e.matrixRefs = nil
	root, err := e.workflow(c)
	if err != nil {
		return nil, err
	}
	if err := e.markStrayAxes(c); err != nil {
		return nil, err
	}
	if root.Content == nil {
		return nil, fmt.Errorf("workflow %s has no content", c.name)
	}
	return marshalDoc(root, e.head)
}
