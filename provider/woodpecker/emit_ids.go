package woodpecker

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
)

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
		for _, j := range allJobs(pl) {
			bases = append(bases, baseName(pls, pl, j))
		}
	}
	assigned := roundtrip.AssignIDs(dialect, bases)
	seen := map[string]string{}
	out := make([]map[string]string, len(pls))
	for i, pl := range pls {
		out[i] = map[string]string{}
		for _, j := range allJobs(pl) {
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

// EmittedIDs lists what Emit renames in a single-pipeline project: the pipeline, which Parse always calls
// woodpecker, and each job whose id is not a valid workflow name. Several pipelines fold into prefixed
// workflow names and carry markers instead.
func (adapter) EmittedIDs(p *ir.Project) map[string]string {
	pls := livePipelines(p)
	if len(pls) != 1 {
		return nil
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
	return out
}
