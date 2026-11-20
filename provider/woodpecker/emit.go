package woodpecker

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

const workflowExt = ".yaml"

// Emit writes one workflow file per job under .woodpecker/. Every feature use goes through the ledger;
// Finish fails the call when one was never accounted for.
func (a adapter) Emit(p *ir.Project) (map[string][]byte, *report.Report, error) {
	origin := ""
	if p != nil {
		origin = p.Origin.Provider
	}
	e := &emitter{from: sourceDialect(origin), own: origin == dialect, led: emitkit.NewLedger("ir", dialect, a.Caps())}
	uses := ir.Features(p)
	for _, u := range uses {
		e.led.Decide(u)
	}
	files, err := e.project(livePipelines(p))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", dialect, err)
	}
	rep, err := e.led.Finish(uses)
	if err != nil {
		return nil, rep, fmt.Errorf("%s: %w", dialect, err)
	}
	return files, rep, nil
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
		e.loseName(pp, pl.Name, pl.ID)
		for _, j := range allJobs(pl) {
			c := &wfCtx{
				pl: pl, j: j, names: names[i], uploads: map[string]bool{}, jp: pp + "/job/" + j.ID,
				name: names[i][j.ID], finally: !containsJob(pl.Jobs, j),
			}
			data, err := e.workflowFile(c, i > 0)
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
func (e *emitter) workflowFile(c *wfCtx, folded bool) ([]byte, error) {
	e.head = nil
	if folded {
		e.mark(caps.ProjectMultiPipeline, "pipeline/"+c.pl.ID)
	}
	root, err := e.workflow(c)
	if err != nil {
		return nil, err
	}
	if root.Content == nil {
		return nil, fmt.Errorf("workflow %s has no content", c.name)
	}
	return marshalDoc(root, e.head)
}
