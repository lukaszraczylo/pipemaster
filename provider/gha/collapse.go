package gha

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// collapseMarkers undoes the emulations another provider's emitter marked in the file, so a pipeline that
// travelled through GHA and came back carries no trace of the detour.
func collapseMarkers(dialect string, pl *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string) {
	carriedDone := map[string]bool{}
	for _, u := range uses {
		if !strings.HasPrefix(u.IRPath, "pipeline/"+pl.ID) {
			continue
		}
		switch u.Feature {
		case caps.JobRunner:
			if j := markedJob(pl, u.IRPath); j != nil && slices.Equal(j.Runner.Labels, []string{defaultRunner}) {
				j.Runner.Labels = nil
			}
		case caps.JobCache, caps.JobArtifacts, caps.JobNeedsArtifacts:
			if !carriedDone[u.IRPath] && len(args[u]) == 1 {
				carriedDone[u.IRPath] = collapseCarried(dialect, markedJob(pl, u.IRPath), args[u][0])
			}
		case caps.PipelineFinally:
			collapseFinally(pl, u.IRPath)
		case caps.JobManual:
			collapseManual(markedJob(pl, u.IRPath))
		case caps.TriggerManual:
			pl.Triggers = slices.DeleteFunc(pl.Triggers, func(t ir.Trigger) bool { return t.Kind == ir.TriggerManual })
		case caps.JobIf:
			if j := markedJob(pl, u.IRPath); j != nil {
				j.If = sourceCond(args[u], j.If)
			}
		case caps.StepIf:
			if st := markedStep(pl, u.IRPath); st != nil {
				st.If = sourceCond(args[u], st.If)
			}
		case caps.JobRetry:
			collapseRetry(markedJob(pl, u.IRPath), args[u])
		case caps.TriggerPush:
			dropMatchAll(pl, ir.TriggerPush, func(t *ir.Trigger) *ir.Filter { return &t.Branches })
		case caps.TriggerTag:
			dropMatchAll(pl, ir.TriggerTag, func(t *ir.Trigger) *ir.Filter { return &t.Tags })
		}
	}
}

func markedJob(pl *ir.Pipeline, path string) *ir.Job {
	id, ok := strings.CutPrefix(path, "pipeline/"+pl.ID+"/job/")
	if !ok {
		return nil
	}
	for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		if j != nil && j.ID == id {
			return j
		}
	}
	return nil
}

func dropMatchAll(pl *ir.Pipeline, kind ir.TriggerKind, pick func(*ir.Trigger) *ir.Filter) {
	for i := range pl.Triggers {
		if pl.Triggers[i].Kind != kind {
			continue
		}
		if f := pick(&pl.Triggers[i]); slices.Equal(f.Include, []string{matchAll}) {
			f.Include = nil
		}
	}
}

// collapseRetry unwraps the retry loop from every step that carries it and restores the job's retry policy.
func collapseRetry(j *ir.Job, args []string) {
	if j == nil || len(args) != 1 {
		return
	}
	var r ir.Retry
	if json.Unmarshal([]byte(args[0]), &r) != nil {
		return
	}
	for _, st := range j.Steps {
		if body, ok := unwrapRetry(st.Script); ok {
			st.Script = body
		}
	}
	j.Retry = &r
}

const (
	retryHead = 5
	retryTail = 6
)

func unwrapRetry(lines []string) ([]string, bool) {
	n := len(lines)
	if n < retryHead+retryTail || !strings.HasPrefix(lines[0], "for pipemaster_try in ") ||
		strings.TrimSpace(lines[retryHead-1]) != "set -e" || strings.TrimSpace(lines[n-retryTail]) != ")" ||
		lines[n-1] != `exit "$pipemaster_rc"` {
		return nil, false
	}
	return slices.Clone(lines[retryHead : n-retryTail]), true
}

// collapseCarried drops the steps that stand in for cache, artifacts and artifact needs and restores the fields.
func collapseCarried(dialect string, j *ir.Job, arg string) bool {
	var c carried
	if j == nil || json.Unmarshal([]byte(arg), &c) != nil || c.Pre+c.Post > len(j.Steps) {
		return false
	}
	j.Steps = slices.Clone(j.Steps[c.Pre : len(j.Steps)-c.Post])
	j.Cache, j.Artifacts = c.Cache, c.Artifacts
	for i := range j.Needs {
		j.Needs[i].Artifacts = slices.Contains(c.Needs, j.Needs[i].JobID)
	}
	for i, st := range j.Steps {
		if x, _ := extMap(st.Ext, dialect)[ExtDerivedID].(bool); x {
			st.ID = derivedID(j.ID, i+1)
		}
	}
	return true
}

// sourceCond reads the condition text the emitter carried; without one the parsed condition stays.
func sourceCond(args []string, keep expr.Expr) expr.Expr {
	for _, a := range args {
		var s srcArg
		if json.Unmarshal([]byte(a), &s) != nil || s.Src == "" {
			continue
		}
		if x, err := expr.ParseCanonical(s.Src); err == nil {
			return x
		}
	}
	return keep
}

func markedStep(pl *ir.Pipeline, path string) *ir.Step {
	rest, ok := strings.CutPrefix(path, "pipeline/"+pl.ID+"/job/")
	if !ok {
		return nil
	}
	jobID, stepID, ok := strings.Cut(rest, "/step/")
	if !ok {
		return nil
	}
	if j := markedJob(pl, "pipeline/"+pl.ID+"/job/"+jobID); j != nil {
		for _, st := range j.Steps {
			if st.ID == stepID {
				return st
			}
		}
	}
	return nil
}

// collapseManual turns the dispatch guard back into the manual flag.
func collapseManual(j *ir.Job) {
	if j == nil || j.If == nil {
		if j != nil {
			j.Manual = true
		}
		return
	}
	guard := expr.String(expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxEvent}}, R: expr.Lit{Kind: expr.LitStr, Str: "workflow_dispatch"}})
	switch b, isAnd := j.If.(expr.Binary); {
	case expr.String(j.If) == guard:
		j.If, j.Manual = nil, true
	case isAnd && b.Op == expr.OpAnd && expr.String(b.R) == guard:
		j.If, j.Manual = b.L, true
	}
}

// collapseFinally moves a job the emitter wrote as an always() job back to the pipeline's finally list.
func collapseFinally(pl *ir.Pipeline, path string) {
	j := markedJob(pl, path)
	if j == nil || !slices.Contains(pl.Jobs, j) {
		return
	}
	always := expr.Call{Name: "always"}
	switch b, isAnd := j.If.(expr.Binary); {
	case j.If == nil:
		return
	case expr.String(j.If) == expr.String(always):
		j.If = nil
	case isAnd && b.Op == expr.OpAnd && expr.String(b.L) == expr.String(always):
		j.If = b.R
	default:
		return
	}
	pl.Jobs = slices.DeleteFunc(pl.Jobs, func(o *ir.Job) bool { return o == j })
	pl.Finally = append(pl.Finally, j)
}
