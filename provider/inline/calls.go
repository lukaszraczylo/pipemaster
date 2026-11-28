package inline

import (
	"fmt"
	"slices"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// gateSuffix names the job that stands for the trigger job when the child pipeline is not created.
const gateSuffix = "-created"

// gateJob returns the job that fails when the child workflow rules do not create the child pipeline, as GitLab
// fails the trigger job then. It returns nil when the child pipeline always starts.
func (f *flattener) gateJob(j *ir.Job, child *ir.Pipeline, id, kind string, fwd bool) *ir.Job {
	if j.Child.Gate == nil {
		return nil
	}
	g := childCond(j.Child.Gate, kind, f.hidden(j, child, &ir.Job{}, fwd))
	if g == nil {
		return nil
	}
	fail := &ir.Step{ID: "fail", Kind: ir.StepScript, Script: []string{"exit 1"}, If: expr.Unary{Op: expr.OpNot, X: g}}
	if l, ok := g.(expr.Lit); ok {
		fail.If = nil
		if expr.LitTruthy(l) {
			return nil
		}
	}
	return &ir.Job{
		ID: id, If: j.If, Manual: j.Manual, AllowFailure: j.AllowFailure, Needs: slices.Clone(j.Needs),
		Steps: []*ir.Step{fail, {ID: "pass", Kind: ir.StepScript, Script: []string{"true"}}},
	}
}

// addNeed adds a need on the gate job. A nil gate changes nothing.
func addNeed(needs []ir.Need, gate *ir.Job) []ir.Need {
	if gate == nil || slices.ContainsFunc(needs, func(n ir.Need) bool { return n.JobID == gate.ID }) {
		return needs
	}
	return append(needs, ir.Need{JobID: gate.ID})
}

// dropCall removes a call whose child the rewrite cannot carry. The call job stays without its child, so the
// emitter's own fail-loud path reports the work as missing.
func (f *flattener) dropCall(j *ir.Job, path, kind string) {
	if j.Child != nil && j.Child.Pipeline != nil {
		emitkit.MarkCallUnrunnable(j)
	}
	j.Child = nil
	f.res.Entries = append(f.res.Entries, report.Entry{
		IRPath: path, Feature: callFeature(kind), Level: caps.Lost, Strategy: EntryStrategy,
		Detail: "the matrix of the call job meets a matrix of a child job, so the child cannot run inline; the work of the child is missing",
	})
}

// blocked is true for a call job that the rewrite cannot carry: its matrix would meet a matrix of a child job,
// and a job has one matrix.
func blocked(j *ir.Job, child *ir.Pipeline) bool {
	if j.Matrix == nil {
		return false
	}
	return slices.ContainsFunc(append(slices.Clone(child.Jobs), child.Finally...), func(cj *ir.Job) bool { return cj != nil && cj.Matrix != nil })
}

func unique(taken map[string]bool, id string) string {
	cand := id
	for n := 2; taken[cand]; n++ {
		cand = fmt.Sprintf("%s-%d", id, n)
	}
	taken[cand] = true
	return cand
}

// rewire replaces a need on the call job: jobs that wait for a blocking call wait for the sinks of its child;
// jobs that wait for a call that passes at creation wait for what the call waited for.
func rewire(needs []ir.Need, call string, depend bool, sinks, callNeeds []ir.Need) []ir.Need {
	if !slices.ContainsFunc(needs, func(n ir.Need) bool { return n.JobID == call }) {
		return needs
	}
	repl := callNeeds
	if depend && len(sinks) > 0 {
		repl = sinks
	}
	var out []ir.Need
	seen := map[string]bool{}
	add := func(n ir.Need) {
		if !seen[n.JobID] {
			seen[n.JobID] = true
			out = append(out, n)
		}
	}
	for _, n := range needs {
		if n.JobID != call {
			add(n)
			continue
		}
		for _, r := range repl {
			r.Optional = r.Optional || n.Optional
			add(r)
		}
	}
	return out
}

func andExpr(a, b expr.Expr) expr.Expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return expr.Binary{Op: expr.OpAnd, L: a, R: b}
}

// childCond folds the conditions of a GitLab child job for a run that the parent starts.
func childCond(e expr.Expr, kind string, hide *hiding) expr.Expr {
	if e == nil || kind != kindGitLab {
		return e
	}
	e = expr.FoldEvent(hideVars(e, hide), parentSource)
	if l, ok := e.(expr.Lit); ok && expr.LitTruthy(l) {
		return nil
	}
	return e
}
