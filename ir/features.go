package ir

import (
	"sort"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
)

// FeatureUse records one feature used at one IR path.
type FeatureUse struct {
	IRPath  string
	Feature caps.Feature
}

type featureSet struct {
	seen map[FeatureUse]bool
	out  []FeatureUse
}

func (s *featureSet) add(path string, f caps.Feature) {
	u := FeatureUse{IRPath: path, Feature: f}
	if s.seen[u] {
		return
	}
	s.seen[u] = true
	s.out = append(s.out, u)
}

func (s *featureSet) when(cond bool, path string, f caps.Feature) {
	if cond {
		s.add(path, f)
	}
}

var triggerFeatures = map[TriggerKind]caps.Feature{
	TriggerPush:         caps.TriggerPush,
	TriggerPR:           caps.TriggerPullRequest,
	TriggerTag:          caps.TriggerTag,
	TriggerSchedule:     caps.TriggerSchedule,
	TriggerManual:       caps.TriggerManual,
	TriggerAPI:          caps.TriggerAPI,
	TriggerPipelineDone: caps.TriggerPipelineDone,
	TriggerResource:     caps.TriggerResource,
}

// Features lists every feature the project uses, once per IR path, sorted by path then feature.
// Trigger indexes follow slice order, so call Normalize first for stable paths.
func Features(p *Project) []FeatureUse {
	s := &featureSet{seen: map[FeatureUse]bool{}}
	if p != nil {
		first := true
		for _, pl := range p.Pipelines {
			if pl == nil {
				continue
			}
			s.when(!first, pipelinePath(pl.ID), caps.ProjectMultiPipeline)
			first = false
			pipelineFeatures(s, pl)
		}
	}
	sort.Slice(s.out, func(i, j int) bool {
		if s.out[i].IRPath != s.out[j].IRPath {
			return s.out[i].IRPath < s.out[j].IRPath
		}
		return s.out[i].Feature < s.out[j].Feature
	})
	return s.out
}

func pipelineFeatures(s *featureSet, pl *Pipeline) {
	path := pipelinePath(pl.ID)
	s.when(len(pl.Env) > 0, path, caps.PipelineEnv)
	s.when(varsOpaque(pl.Env), path, caps.ExprOpaque)
	s.when(len(pl.Inputs) > 0, path, caps.PipelineInputs)
	s.when(pl.Concurrency != nil, path, caps.PipelineConcurrency)
	s.when(pl.Permissions != nil, path, caps.PipelinePermissions)
	s.when(len(pl.Finally) > 0, path, caps.PipelineFinally)
	s.when(len(pl.Ext) > 0, path, caps.ExtPassthrough)
	for i := range pl.Triggers {
		triggerUse(s, triggerPath(pl.ID, i), &pl.Triggers[i])
	}
	for _, j := range append(append([]*Job{}, pl.Jobs...), pl.Finally...) {
		if j != nil {
			jobFeatures(s, pl.ID, j)
		}
	}
}

func isOpaque(e expr.Expr) bool { return expr.HasOpaque(e) }

func varsOpaque(vs []Var) bool {
	for _, v := range vs {
		if v.Value.Kind == ValueExpr && isOpaque(v.Value.Expr) {
			return true
		}
	}
	return false
}

func hasFilter(f Filter) bool { return len(f.Include) > 0 || len(f.Exclude) > 0 }

func triggerUse(s *featureSet, path string, t *Trigger) {
	if f, ok := triggerFeatures[t.Kind]; ok {
		s.add(path, f)
	}
	s.when(t.Cron != "", path, caps.TriggerSchedule)
	s.when(t.Resource != nil, path, caps.TriggerResource)
	s.when(hasFilter(t.Branches), path, caps.TriggerFilterBranch)
	s.when(hasFilter(t.Tags), path, caps.TriggerFilterTags)
	s.when(hasFilter(t.Paths), path, caps.TriggerFilterPaths)
	s.when(len(t.Ext) > 0, path, caps.ExtPassthrough)
}

func jobFeatures(s *featureSet, pid string, j *Job) {
	path := jobPath(pid, j.ID)
	s.when(len(j.Needs) > 0, path, caps.JobNeeds)
	for _, n := range j.Needs {
		s.when(n.Artifacts, path, caps.JobNeedsArtifacts)
	}
	s.when(j.If != nil, path, caps.JobIf)
	opaque := isOpaque(j.If) || varsOpaque(j.Env)
	for _, sv := range j.Services {
		opaque = opaque || varsOpaque(sv.Env)
	}
	s.when(opaque, path, caps.ExprOpaque)
	r := j.Runner
	s.when(len(r.Labels) > 0 || r.Platform != "" || len(r.Selector) > 0, path, caps.JobRunner)
	s.when(j.Container != nil, path, caps.JobContainer)
	s.when(len(j.Services) > 0, path, caps.JobServices)
	s.when(j.Matrix != nil, path, caps.JobMatrix)
	s.when(j.Timeout != nil, path, caps.JobTimeout)
	s.when(j.Retry != nil, path, caps.JobRetry)
	s.when(j.AllowFailure, path, caps.JobAllowFailure)
	s.when(j.Environment != nil, path, caps.JobEnvironment)
	s.when(j.Environment != nil && j.Environment.Approval, path, caps.JobEnvironmentApproval)
	s.when(j.Manual, path, caps.JobManual)
	s.when(len(j.Cache) > 0, path, caps.JobCache)
	s.when(len(j.Artifacts) > 0, path, caps.JobArtifacts)
	s.when(len(j.Outputs) > 0, path, caps.JobOutputs)
	s.when(len(j.Env) > 0, path, caps.JobEnv)
	s.when(len(j.Ext) > 0, path, caps.ExtPassthrough)
	for _, st := range j.Steps {
		if st != nil {
			stepFeatures(s, stepPath(pid, j.ID, st.ID), st)
		}
	}
}

func stepFeatures(s *featureSet, path string, st *Step) {
	s.when(len(st.Script) > 0, path, caps.StepScript)
	s.when(st.Action != nil || len(st.With) > 0, path, caps.StepAction)
	s.when(st.Image != nil, path, caps.StepImage)
	s.when(st.If != nil, path, caps.StepIf)
	opaque := isOpaque(st.If) || varsOpaque(st.Env)
	for _, v := range st.With {
		opaque = opaque || (v.Kind == ValueExpr && isOpaque(v.Expr))
	}
	s.when(opaque, path, caps.ExprOpaque)
	s.when(len(st.Env) > 0, path, caps.StepEnv)
	s.when(st.ContinueOnError, path, caps.StepContinueOnError)
	s.when(st.Timeout != nil, path, caps.StepTimeout)
	s.when(st.WorkDir != "", path, caps.StepWorkdir)
	s.when(st.Shell != "", path, caps.StepShell)
	s.when(len(st.Ext) > 0, path, caps.ExtPassthrough)
}
