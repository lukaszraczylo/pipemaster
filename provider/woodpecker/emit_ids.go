package woodpecker

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// emptyJobID names the workflow that holds a pipeline with no job: Woodpecker needs a file to carry its triggers and variables.
const emptyJobID = "empty"

// workflowJobs is the jobs a pipeline writes as workflows; a pipeline with none writes one placeholder workflow.
func workflowJobs(pl *ir.Pipeline) []*ir.Job {
	if jobs := allJobs(pl); len(jobs) > 0 {
		return jobs
	}
	return []*ir.Job{{ID: emptyJobID}}
}

func allJobs(pl *ir.Pipeline) []*ir.Job {
	var out []*ir.Job
	for _, js := range [][]*ir.Job{pl.Jobs, pl.Finally} {
		for _, j := range js {
			if j != nil {
				out = append(out, j)
			}
		}
	}
	return out
}

// workflowNames maps, per pipeline, each job id to the workflow name it is written under. One pipeline keeps
// its job ids; several pipelines prefix the pipeline id so jobs of different pipelines cannot collide.
func workflowNames(pls []*ir.Pipeline) ([]map[string]string, error) {
	var bases []string
	for _, pl := range pls {
		for _, j := range workflowJobs(pl) {
			bases = append(bases, baseName(pls, pl, j))
		}
	}
	assigned := roundtrip.AssignIDs(dialect, bases)
	seen := map[string]string{}
	out := make([]map[string]string, len(pls))
	for i, pl := range pls {
		out[i] = map[string]string{}
		for _, j := range workflowJobs(pl) {
			base := baseName(pls, pl, j)
			name := assigned[base]
			if prev, dup := seen[name]; dup {
				return nil, fmt.Errorf("workflow name %q is used by %s and %s", name, prev, base)
			}
			seen[name] = base
			out[i][j.ID] = name
		}
	}
	return out, nil
}

func baseName(pls []*ir.Pipeline, pl *ir.Pipeline, j *ir.Job) string {
	if len(pls) > 1 {
		return pl.ID + "-" + j.ID
	}
	return j.ID
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

// EmittedIDs lists what Emit renames: in a single-pipeline project the pipeline, which Parse always calls
// woodpecker, and each job whose id is not a valid workflow name; in every project the steps that Parse names
// by their place.
func (a adapter) EmittedIDs(p *ir.Project) map[string]string {
	pls := livePipelines(p)
	if len(pls) != 1 {
		// Parse splits several pipelines by their markers and gives back the job IDs, so only the steps differ.
		_, _, steps, err := a.emit(p)
		if err != nil {
			return nil
		}
		return steps
	}
	pl := pls[0]
	out := map[string]string{}
	if pl.ID != pipelineID {
		out[pipelinePth] = pl.ID
	}
	names, err := workflowNames(pls)
	if err != nil {
		return out
	}
	for id, name := range names[0] {
		if id != name {
			out[pipelinePth+"/job/"+name] = id
		}
	}
	_, _, steps, err := a.emit(p)
	if err != nil {
		return out
	}
	for _, j := range workflowJobs(pl) {
		prefix := "pipeline/" + pl.ID + "/job/" + j.ID + "/step/"
		for path, src := range steps {
			if rest, ok := strings.CutPrefix(path, prefix); ok {
				out[pipelinePth+"/job/"+names[0][j.ID]+"/step/"+rest] = src
			}
		}
	}
	return out
}
