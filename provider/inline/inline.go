// Package inline rewrites a project that holds child calls into a flat project for a target that has no native
// child construct. Every emitter runs it first through Emit, so none can forget a call.
package inline

import (
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// Source dialect keys and GitLab trigger keys that the rewrite reads.
const (
	extGitLab       = "gitlab"
	extTrigger      = "trigger"
	extStrategy     = "strategy"
	extForward      = "forward"
	extYAMLVars     = "yaml_variables"
	extPipelineVars = "pipeline_variables"
	strategyWait    = "depend"
	parentSource    = "parent_pipeline"
	idJoin          = "-"
	derivedStep     = "-step-"
	kindGitLab      = "gitlab"
	kindReusable    = "reusable workflow"
)

// EntryStrategy is the strategy of every report entry that an inline writes.
const EntryStrategy = "inline child jobs"

// Result is what a rewrite did: the report entries, and the record of each top-level call for the metadata.
type Result struct {
	Calls   map[string]meta.Call
	Entries []report.Entry
}

// Emit runs emit on the project that Flatten returns and adds the entries of the rewrite to its report.
func Emit(p *ir.Project, target string, emit func(*ir.Project) (map[string][]byte, *report.Report, error)) (map[string][]byte, *report.Report, error) {
	flat, res := Flatten(p, target)
	flat, docker, _ := DockerSteps(flat, target)
	res.Entries = append(res.Entries, docker...)
	files, rep, err := emit(flat)
	if rep != nil {
		rep.Entries = append(rep.Entries, res.Entries...)
	}
	return files, rep, err
}

// DockerSteps rewrites the docker:// steps of p that target cannot run as actions, which is every target outside
// the GHA family. It returns the rewritten steps as the source had them, by IR path.
func DockerSteps(p *ir.Project, target string) (*ir.Project, []report.Entry, map[string]*ir.Step) {
	if provider.Family(target) == provider.GHA {
		return p, nil, nil
	}
	return emitkit.DockerSteps(p)
}

// Native reports whether the target writes the child as the construct it came from.
func Native(c *ir.Child, target string) bool { return c != nil && slices.Contains(c.Native, target) }

// HasCalls reports whether any pipeline of p holds a job with a child that the target cannot write natively, or a
// child of a job that Flatten cannot reach.
func HasCalls(p *ir.Project, target string) bool {
	if p == nil {
		return false
	}
	for _, pl := range p.Pipelines {
		if pl == nil {
			continue
		}
		for _, j := range append(slices.Clone(pl.Jobs), pl.Finally...) {
			if j != nil && j.Child != nil && !Native(j.Child, target) {
				return true
			}
		}
	}
	return false
}

// Flatten returns p with the jobs of every child that target cannot write natively in place of the call job. It
// returns p itself when there is nothing to rewrite, and never changes p.
func Flatten(p *ir.Project, target string) (*ir.Project, Result) {
	res := Result{Calls: map[string]meta.Call{}}
	if !HasCalls(p, target) {
		return p, res
	}
	out := ir.Clone(p)
	for _, pl := range out.Pipelines {
		if pl == nil {
			continue
		}
		f := &flattener{pl: pl, target: target, res: &res, orig: map[string][]ir.Need{}}
		f.run()
	}
	return out, res
}

type flattener struct {
	pl     *ir.Pipeline
	res    *Result
	orig   map[string][]ir.Need
	target string
}

// run inlines each call of the pipeline once. Jobs that an inline adds join the queue, so a nested call is
// rewritten after the call that holds it.
func (f *flattener) run() {
	for _, j := range append(slices.Clone(f.pl.Jobs), f.pl.Finally...) {
		if j != nil {
			f.orig[j.ID] = slices.Clone(j.Needs)
		}
	}
	queue := slices.Clone(f.pl.Jobs)
	owner := map[string]string{}
	top := map[string]*meta.Call{}
	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]
		if j == nil || j.Child == nil {
			continue
		}
		if Native(j.Child, f.target) {
			continue
		}
		root := owner[j.ID]
		if root == "" {
			root = j.ID
			rec := &meta.Call{Job: ir.Clone(&ir.Project{Pipelines: []*ir.Pipeline{{Jobs: []*ir.Job{j}}}}).Pipelines[0].Jobs[0]}
			rec.Job.Needs = slices.Clone(f.orig[j.ID])
			top[root] = rec
		}
		added := f.inline(j, owner, root, top)
		queue = append(queue, added...)
	}
	for root, rec := range top {
		rec.Needs = map[string][]ir.Need{}
		live := map[string]*ir.Job{}
		for _, j := range append(slices.Clone(f.pl.Jobs), f.pl.Finally...) {
			if j != nil {
				live[j.ID] = j
			}
		}
		for id, was := range f.orig {
			if j, ok := live[id]; ok && !slices.Equal(j.Needs, was) {
				rec.Needs[id] = was
			}
		}
		rec.Inlined = slices.DeleteFunc(rec.Inlined, func(id string) bool { return live[id] == nil })
		f.res.Calls[jobPath(f.pl.ID, root)] = *rec
	}
}

func jobPath(pid, jid string) string { return "pipeline/" + pid + "/job/" + jid }

// inline replaces the call job j with the jobs of its child and returns the jobs it added.
func (f *flattener) inline(j *ir.Job, owner map[string]string, root string, top map[string]*meta.Call) []*ir.Job {
	pl := f.pl
	child := j.Child.Pipeline
	kind := callKind(j)
	path := jobPath(pl.ID, j.ID)
	if child == nil || blocked(j, child) {
		f.dropCall(j, path, kind)
		return nil
	}
	taken := map[string]bool{}
	for _, o := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		if o != nil {
			taken[o.ID] = true
		}
	}
	delete(taken, j.ID)
	ids := map[string]string{}
	for _, cj := range append(slices.Clone(child.Jobs), child.Finally...) {
		if cj != nil {
			ids[cj.ID] = unique(taken, j.ID+idJoin+cj.ID)
		}
	}
	args := callArgs(j, child)
	depend := kind != kindGitLab || triggerStrategy(j) == strategyWait
	fwd := forwardsVars(j)
	gate := f.gateJob(j, child, unique(taken, j.ID+gateSuffix), kind, fwd)
	if gate == nil {
		delete(taken, j.ID+gateSuffix)
	}
	var added, sinks []*ir.Job
	needed := map[string]bool{}
	for _, cj := range child.Jobs {
		if cj == nil {
			continue
		}
		for _, n := range cj.Needs {
			needed[n.JobID] = true
		}
	}
	rename := map[string]string{}
	for o, n := range ids {
		rename[o] = n
	}
	mk := func(cj *ir.Job, finally bool) *ir.Job {
		nj := ir.Clone(&ir.Project{Pipelines: []*ir.Pipeline{{Jobs: []*ir.Job{cj}}}}).Pipelines[0].Jobs[0]
		nj.ID = ids[cj.ID]
		for _, st := range nj.Steps {
			// A step id that the parser derived from the job id follows the job, as a parse of the output derives it.
			if st != nil && strings.HasPrefix(st.ID, cj.ID+derivedStep) {
				st.ID = nj.ID + strings.TrimPrefix(st.ID, cj.ID)
			}
		}
		inner := nj.Child
		nj.Child = nil
		emitkit.RewriteNeedsRefs(nj, rename)
		nj.Child = inner
		inside := false
		for i := range nj.Needs {
			if id, ok := ids[nj.Needs[i].JobID]; ok {
				nj.Needs[i].JobID = id
				inside = true
			}
		}
		if !inside && !finally {
			nj.Needs = append(nj.Needs, slices.Clone(j.Needs)...)
			nj.Needs = addNeed(nj.Needs, gate)
		}
		hide := f.hidden(j, child, nj, fwd)
		nj.If = andExpr(j.If, childCond(andExpr(j.Child.Gate, cj.If), kind, hide))
		nj.Manual = nj.Manual || j.Manual
		nj.AllowFailure = nj.AllowFailure || j.AllowFailure
		if nj.Matrix == nil && j.Matrix != nil {
			m := *j.Matrix
			nj.Matrix = &m
		}
		nj.Env = mergeEnv(mergeEnv(child.Env, nj.Env), callEnv(j, fwd))
		hiddenVars := hiddenEnv(hide, pl.Env, nj.Env)
		nj.Env = append(nj.Env, hiddenVars...)
		f.unsetInScripts(nj, hiddenVars)
		args.apply(nj)
		return nj
	}
	for _, cj := range child.Jobs {
		if cj == nil {
			continue
		}
		nj := mk(cj, false)
		added = append(added, nj)
		if !needed[cj.ID] {
			sinks = append(sinks, nj)
		}
	}
	var finally []*ir.Job
	for _, cj := range child.Finally {
		if cj != nil {
			finally = append(finally, mk(cj, true))
		}
	}
	sinkNeeds := make([]ir.Need, 0, len(sinks))
	for _, s := range sinks {
		sinkNeeds = append(sinkNeeds, ir.Need{JobID: s.ID})
	}
	callNeeds := slices.Clone(j.Needs)
	for _, o := range append(slices.Clone(pl.Jobs), pl.Finally...) {
		if o == nil || o == j {
			continue
		}
		before := o.Needs
		o.Needs = rewire(o.Needs, j.ID, depend, sinkNeeds, callNeeds)
		if len(before) != len(o.Needs) || !slices.Equal(before, o.Needs) {
			o.Needs = addNeed(o.Needs, gate)
		}
	}
	pl.Jobs = slices.DeleteFunc(pl.Jobs, func(o *ir.Job) bool { return o == j })
	if gate != nil {
		added = append([]*ir.Job{gate}, added...)
	}
	pl.Jobs = append(pl.Jobs, added...)
	pl.Finally = append(pl.Finally, finally...)
	rec := top[root]
	if root == j.ID {
		rec.Inlined = nil
	}
	for _, a := range append(slices.Clone(added), finally...) {
		owner[a.ID] = root
		rec.Inlined = append(rec.Inlined, a.ID)
	}
	f.report(j, path, kind, child, added, finally, depend, fwd, args, gate)
	return added
}

// mergeEnv returns base without the names that over sets, then over.
func mergeEnv(base, over []ir.Var) []ir.Var {
	var out []ir.Var
	for _, v := range base {
		if !slices.ContainsFunc(over, func(o ir.Var) bool { return o.Name == v.Name }) {
			out = append(out, v)
		}
	}
	return append(out, over...)
}

func callKind(j *ir.Job) string {
	if own, _ := j.Ext[extGitLab].(map[string]any); own != nil {
		if _, ok := own[extTrigger]; ok {
			return kindGitLab
		}
	}
	return kindReusable
}

func callFeature(kind string) caps.Feature {
	if kind == kindGitLab {
		return caps.ExtPassthrough
	}
	return caps.StepAction
}

func trigger(j *ir.Job) map[string]any {
	own, _ := j.Ext[extGitLab].(map[string]any)
	t, _ := own[extTrigger].(map[string]any)
	return t
}

func triggerStrategy(j *ir.Job) string {
	s, _ := trigger(j)[extStrategy].(string)
	return s
}

// forwardsVars reports whether the trigger variables reach the child: GitLab forwards them unless forward:
// yaml_variables is false.
func forwardsVars(j *ir.Job) bool {
	f, _ := trigger(j)[extForward].(map[string]any)
	if v, ok := f[extYAMLVars].(bool); ok {
		return v
	}
	return true
}

func callEnv(j *ir.Job, forward bool) []ir.Var {
	if callKind(j) != kindGitLab || !forward {
		return nil
	}
	return j.Env
}
