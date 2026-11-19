package gitlab

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	stageNamePrefix = "stage-"
	extStages       = "stages"
	extNeedsKey     = "needs"
	extDeps         = "dependencies"
)

// stagePlan says which stage each job gets and which jobs leave their needs to the stage order.
type stagePlan struct {
	stage   map[string]string
	derived map[string]bool
	list    []string
}

func stageList(v any) ([]string, bool) {
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, it := range l {
		s, ok := it.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func stageIndex(declared []string) map[string]int {
	idx := map[string]int{}
	for i, st := range append(append([]string{stagePre}, declared...), stagePost) {
		if _, dup := idx[st]; !dup {
			idx[st] = i
		}
	}
	return idx
}

func allJobs(pl *ir.Pipeline) []*ir.Job {
	var out []*ir.Job
	for _, j := range append(append([]*ir.Job{}, pl.Jobs...), pl.Finally...) {
		if j != nil {
			out = append(out, j)
		}
	}
	return out
}

// stagePlan chooses the stages of pl; a folded pipeline always uses DAG layers, since its jobs come from several pipelines.
func (e *emitter) stagePlan(pl *ir.Pipeline) (*stagePlan, error) {
	if e.fold != nil {
		return layerStages(pl)
	}
	return planStages(pl)
}

// planStages restores the stages a GitLab file declared when they still respect every needs edge,
// and otherwise builds stage-N names from the DAG layers.
func planStages(pl *ir.Pipeline) (*stagePlan, error) {
	if plan, ok := restoreStages(pl); ok {
		return plan, nil
	}
	return layerStages(pl)
}

func jobStage(j *ir.Job) (name string, written bool) {
	if s, ok := extOf(j.Ext)["stage"].(string); ok {
		return s, true
	}
	return defaultStage, false
}

func restoreStages(pl *ir.Pipeline) (*stagePlan, bool) {
	ext := extOf(pl.Ext)
	declared, hasStages := stageList(ext[extStages])
	if !hasStages {
		declared = defaultStages
	}
	idx := stageIndex(declared)
	jobs := allJobs(pl)
	if inc, _ := ext[extInclude].([]any); len(inc) > 0 {
		// A remote template declares the stages the file does not; they sort before .post, as Parse reads them.
		for _, j := range jobs {
			if st, _ := jobStage(j); idx[st] == 0 && st != stagePre {
				idx[st] = idx[stagePost]
				idx[stagePost]++
			}
		}
	}
	finally := map[string]bool{}
	for _, f := range pl.Finally {
		if f != nil {
			finally[f.ID] = true
		}
	}
	// A GitLab file may have a job need one in its own stage; a pipeline with no stage layout of its own
	// (from another dialect) keeps strictly earlier stages.
	sameStage := 0
	if hasStages {
		sameStage = 1
	}
	for _, j := range jobs {
		if _, written := jobStage(j); written {
			sameStage = 1
		}
	}
	byID := map[string]*ir.Job{}
	stageOf := map[string]string{}
	forced := map[string]bool{}
	last := 0
	for _, j := range jobs {
		st, _ := jobStage(j)
		i, ok := idx[st]
		if !ok && !finally[j.ID] {
			return nil, false
		}
		if !finally[j.ID] {
			last = max(last, i)
		}
		byID[j.ID], stageOf[j.ID] = j, st
	}
	// A finally job that names no stage must still run after every other job, so it goes to .post when its
	// default stage would let it start earlier.
	for _, j := range jobs {
		if _, written := jobStage(j); finally[j.ID] && !written {
			if i, ok := idx[stageOf[j.ID]]; !ok || i < last {
				stageOf[j.ID], forced[j.ID] = stagePost, true
			}
		}
	}
	for _, j := range jobs {
		if _, ok := idx[stageOf[j.ID]]; !ok {
			return nil, false
		}
		for _, n := range j.Needs {
			if t, ok := byID[n.JobID]; ok && idx[stageOf[t.ID]] >= idx[stageOf[j.ID]]+sameStage {
				return nil, false
			}
		}
	}
	plan := &stagePlan{stage: map[string]string{}, derived: map[string]bool{}}
	if hasStages {
		plan.list = declared
	}
	for _, j := range jobs {
		if _, written := jobStage(j); written || forced[j.ID] {
			plan.stage[j.ID] = stageOf[j.ID]
		}
		if d, _ := extOf(j.Ext)[ExtDerivedNeeds].(bool); d {
			plan.derived[j.ID] = sameNeeds(j, jobs, finally, idx, stageOf)
		}
	}
	return plan, true
}

// sameNeeds reports whether j's needs are exactly the non-finally jobs of earlier stages.
func sameNeeds(j *ir.Job, jobs []*ir.Job, finally map[string]bool, idx map[string]int, stageOf map[string]string) bool {
	want := map[string]bool{}
	for _, o := range jobs {
		if o != j && !finally[o.ID] && idx[stageOf[o.ID]] < idx[stageOf[j.ID]] {
			want[o.ID] = true
		}
	}
	if len(want) != len(j.Needs) {
		return false
	}
	for _, n := range j.Needs {
		if !want[n.JobID] {
			return false
		}
	}
	return true
}

func layerStages(pl *ir.Pipeline) (*stagePlan, error) {
	layers, err := ir.Layers(pl.Jobs)
	if err != nil {
		return nil, fmt.Errorf("pipeline %s: %w", pl.ID, err)
	}
	plan := &stagePlan{stage: map[string]string{}, derived: map[string]bool{}}
	if len(layers) > 1 {
		for i, ids := range layers {
			name := fmt.Sprintf("%s%d", stageNamePrefix, i)
			plan.list = append(plan.list, name)
			for _, id := range ids {
				plan.stage[id] = name
			}
		}
	}
	for _, f := range pl.Finally {
		if f != nil {
			plan.stage[f.ID] = stagePost
		}
	}
	return plan, nil
}

// needsOf lists the jobs j names, for the lost-edge checks.
func needsOf(j *ir.Job) string {
	ids := make([]string, 0, len(j.Needs))
	for _, n := range j.Needs {
		ids = append(ids, n.JobID)
	}
	return strings.Join(ids, ", ")
}
