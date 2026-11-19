package gha

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

// collapseMarkers undoes the emulations another provider's emitter marked in the file, so a pipeline that
// travelled through GHA and came back carries no trace of the detour.
func collapseMarkers(e *emitter, pl *ir.Pipeline, uses []ir.FeatureUse, args map[ir.FeatureUse][]string, rep *report.Report) {
	carriedDone := map[string]bool{}
	finallyPaths := map[string]bool{}
	for _, u := range uses {
		if u.Feature == caps.PipelineFinally {
			finallyPaths[u.IRPath] = true
		}
	}
	// Condition markers run last: they compare against the condition once the guards are gone.
	slices.SortStableFunc(uses, func(a, b ir.FeatureUse) int { return condRank(a.Feature) - condRank(b.Feature) })
	for _, u := range uses {
		if u.IRPath != "pipeline/"+pl.ID && !strings.HasPrefix(u.IRPath, "pipeline/"+pl.ID+"/") {
			continue
		}
		switch u.Feature {
		case caps.StepScript:
			if u.IRPath == "pipeline/"+pl.ID {
				pl.Jobs = slices.DeleteFunc(pl.Jobs, func(j *ir.Job) bool {
					return j.ID == emptyJobID && len(j.Steps) == 1 && slices.Equal(j.Steps[0].Script, []string{noopScript})
				})
				break
			}
			if j := markedJob(pl, u.IRPath); j != nil && len(j.Steps) == 1 && slices.Equal(j.Steps[0].Script, []string{noopScript}) {
				j.Steps = nil
			}
		case caps.JobRunner:
			if j := markedJob(pl, u.IRPath); j != nil && slices.Equal(j.Runner.Labels, []string{defaultRunner}) {
				j.Runner.Labels = nil
			}
		case caps.JobCache, caps.JobArtifacts, caps.JobNeedsArtifacts:
			if !carriedDone[u.IRPath] && len(args[u]) == 1 {
				carriedDone[u.IRPath] = collapseCarried(provider.Family(e.d.ID), markedJob(pl, u.IRPath), args[u][0])
			}
		case caps.PipelineFinally:
			collapseFinally(pl, rep, u, args[u], finallyPaths)
		case caps.JobManual:
			collapseManual(markedJob(pl, u.IRPath))
		case caps.PipelineInputs:
			pl.Triggers = slices.DeleteFunc(pl.Triggers, func(t ir.Trigger) bool { return t.Kind == ir.TriggerManual })
		case caps.PipelineConcurrency:
			if u.IRPath == "pipeline/"+pl.ID && pl.Concurrency != nil && *pl.Concurrency == (ir.Concurrency{Group: defaultGroup}) {
				pl.Concurrency = nil
			}
		case caps.TriggerManual:
			pl.Triggers = slices.DeleteFunc(pl.Triggers, func(t ir.Trigger) bool { return t.Kind == ir.TriggerManual })
		case caps.JobIf:
			if j := markedJob(pl, u.IRPath); j != nil {
				j.If = sourceCond(rep, u, args[u], j.If)
			}
		case caps.StepIf:
			if st := markedStep(pl, u.IRPath); st != nil {
				st.If = sourceCond(rep, u, args[u], st.If)
			}
		case caps.StepImage:
			collapseImage(e, pl, rep, u, args[u])
		case caps.StepAction:
			collapseHost(e, markedStep(pl, u.IRPath), args[u])
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

func condRank(f caps.Feature) int {
	if f == caps.JobIf || f == caps.StepIf {
		return 1
	}
	return 0
}

// sourceCond reads the condition text the emitter carried. It applies only when writing that text again
// gives the condition parsed from the file; a hand-edited condition stays and the report says so.
func sourceCond(rep *report.Report, u ir.FeatureUse, args []string, parsed expr.Expr) expr.Expr {
	for _, a := range args {
		var s srcArg
		if json.Unmarshal([]byte(a), &s) != nil || s.Src == "" {
			continue
		}
		x, err := expr.ParseCanonical(s.Src)
		if err != nil {
			continue
		}
		if parsed != nil {
			if r, ok := restoreCond(x, parsed); ok {
				parsed = r
				continue
			}
		}
		rep.Add(report.Entry{IRPath: u.IRPath, Feature: u.Feature, Level: caps.Lost, Strategy: "marker ignored",
			Detail: "marker ignored: the condition was edited after conversion, so the parsed condition stands"})
	}
	return parsed
}

// restoreCond puts src in place of the part of parsed it stands for. The emitter marks each side of a
// status guard such as `failure() && cond` on its own, so the status call stays and only the other side is
// replaced. ok is false when the emitter would not have written parsed from src.
func restoreCond(src, parsed expr.Expr) (expr.Expr, bool) {
	if b, ok := parsed.(expr.Binary); ok && b.Op == expr.OpAnd {
		switch {
		case isStatus(b.L) && !isStatus(b.R):
			r, ok := restoreCond(src, b.R)
			return expr.Binary{Op: b.Op, L: b.L, R: r}, ok
		case isStatus(b.R) && !isStatus(b.L):
			l, ok := restoreCond(src, b.L)
			return expr.Binary{Op: b.Op, L: l, R: b.R}, ok
		}
	}
	if rederives(src, parsed) {
		return src, true
	}
	return nil, false
}

// rederives is true when the emitter would write src as the condition parsed.
func rederives(src, parsed expr.Expr) bool {
	t, _, ok := expr.TranslateApprox(src, exprgitlab.Lang, exprgha.Lang)
	if !ok {
		return false
	}
	text, err := printExpr(t)
	if err != nil {
		return false
	}
	back, err := exprgha.Parse(text)
	return err == nil && expr.String(back) == expr.String(parsed)
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
	case j.If == expr.Lit{Kind: expr.LitBool}:
		j.Manual = true
	}
}

// collapseFinally moves a job the emitter wrote as an always() job back to the pipeline's finally list.
func collapseFinally(pl *ir.Pipeline, rep *report.Report, u ir.FeatureUse, args []string, finallyPaths map[string]bool) {
	j := markedJob(pl, u.IRPath)
	if j == nil || !slices.Contains(pl.Jobs, j) {
		return
	}
	always := expr.Call{Name: "always"}
	switch b, isAnd := j.If.(expr.Binary); {
	case j.If == nil:
		return
	case j.If == expr.Lit{Kind: expr.LitBool}:
		// A condition GHA cannot state disables the job and drops the guard; the job condition marker restores it.
	case expr.String(j.If) == expr.String(always):
		j.If = nil
	case isAnd && b.Op == expr.OpAnd && expr.String(b.L) == expr.String(always):
		j.If = b.R
	default:
		return
	}
	restoreFinallyNeeds(pl, rep, u, args, j, finallyPaths)
	pl.Jobs = slices.DeleteFunc(pl.Jobs, func(o *ir.Job) bool { return o == j })
	pl.Finally = append(pl.Finally, j)
}

// restoreFinallyNeeds puts back the needs a finally job had before the emitter added every main job. The
// parsed needs must be exactly what the emitter writes for the marked ones; an edited list stays as parsed.
func restoreFinallyNeeds(pl *ir.Pipeline, rep *report.Report, u ir.FeatureUse, args []string, j *ir.Job, finallyPaths map[string]bool) {
	for _, a := range args {
		var f finallyArg
		if json.Unmarshal([]byte(a), &f) != nil {
			continue
		}
		want := map[string]bool{}
		for _, o := range pl.Jobs {
			if !finallyPaths[jobPath(pl.ID, o.ID)] {
				want[o.ID] = true
			}
		}
		for _, n := range f.Needs {
			want[n.JobID] = true
		}
		got := map[string]bool{}
		for _, n := range j.Needs {
			got[n.JobID] = true
		}
		if !maps.Equal(want, got) {
			rep.Add(report.Entry{IRPath: u.IRPath, Feature: u.Feature, Level: caps.Lost, Strategy: "marker ignored",
				Detail: "marker ignored: the job's needs were edited after conversion, so the parsed needs stand"})
			return
		}
		j.Needs = f.Needs
		return
	}
}

// collapseImage turns a docker action back into an image step when writing the marked step again gives the
// parsed one; an edited step stays as parsed and the report says so.
func collapseImage(e *emitter, pl *ir.Pipeline, rep *report.Report, u ir.FeatureUse, args []string) {
	st := markedStep(pl, u.IRPath)
	var a imageArg
	if st == nil || len(args) != 1 || json.Unmarshal([]byte(args[0]), &a) != nil || st.Action == nil || st.Action.Kind != ir.ActionDocker {
		return
	}
	ref, entrypoint, argText, err := e.imageParts(u.IRPath, &ir.Step{Image: &a.Image, Script: a.Script})
	lit := func(k string) string { return st.With[k].Lit }
	if err != nil || st.Action.Image != ref || lit("entrypoint") != entrypoint || lit("args") != argText {
		rep.Add(report.Entry{IRPath: u.IRPath, Feature: u.Feature, Level: caps.Lost, Strategy: "marker ignored",
			Detail: "marker ignored: the docker step was edited after conversion, so the parsed step stands"})
		return
	}
	with := map[string]ir.Value{}
	for k, v := range st.With {
		if k != "entrypoint" && k != "args" {
			with[k] = v
		}
	}
	if len(with) == 0 {
		with = nil
	}
	st.Kind, st.Action, st.With, st.Image, st.Script = ir.StepScript, nil, with, &a.Image, a.Script
}

// collapseHost restores the host a bare ref had in its source when the parsed step still holds the dialect default.
func collapseHost(e *emitter, st *ir.Step, args []string) {
	var a hostArg
	if st == nil || len(args) != 1 || json.Unmarshal([]byte(args[0]), &a) != nil || !ir.ValidHost(a.Host) || a.Host == "" {
		return
	}
	if st.Action != nil && st.Action.Kind == ir.ActionRepo && st.Action.Host == e.d.DefaultHost {
		st.Action.Host = a.Host
	}
}
