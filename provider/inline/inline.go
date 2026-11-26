// Package inline rewrites a project that holds child calls into a flat project for a target that has no native
// child construct. Every emitter runs it first through Emit, so none can forget a call.
package inline

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/meta"
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
	files, rep, err := emit(flat)
	if rep != nil {
		rep.Entries = append(rep.Entries, res.Entries...)
	}
	return files, rep, err
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
		unsetInScripts(nj, hiddenVars)
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

func (f *flattener) report(j *ir.Job, path, kind string, child *ir.Pipeline, added, finally []*ir.Job, depend, fwd bool, args *callInputs, gate *ir.Job) {
	ids := make([]string, 0, len(added)+len(finally))
	for _, a := range append(slices.Clone(added), finally...) {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	files := child.ID
	if j.Child != nil && len(j.Child.Files) > 0 {
		files = strings.Join(j.Child.Files, ", ")
	}
	detail := fmt.Sprintf("the child pipeline %s runs inline as jobs %s", files, strings.Join(ids, ", "))
	if len(ids) == 0 {
		detail = fmt.Sprintf("the child pipeline %s has no jobs", files)
	}
	var caveats []string
	if gate != nil {
		caveats = append(caveats, "the job "+gate.ID+" fails when the workflow rules of the child pipeline do not create it, as GitLab fails the trigger job, and every job that needs the call waits for it")
	}
	if !depend {
		caveats = append(caveats, "without strategy: depend the trigger job passes when the child pipeline starts, so jobs that need it wait for its own needs only; a failed child job now fails the pipeline")
	}
	if kind == kindGitLab && fwd && len(j.Env) > 0 {
		caveats = append(caveats, "the trigger variables are variables of each child job")
	}
	if len(f.pl.Env) > 0 {
		if kind == kindGitLab && fwd {
			caveats = append(caveats, "the variables of the calling pipeline reach the child jobs, as the trigger forwards them")
		} else {
			caveats = append(caveats, "the variables of the calling pipeline are set empty in the child jobs, as the child does not receive them")
		}
	}
	if kind == kindGitLab && !forwardsPipelineVars(j) {
		caveats = append(caveats, "a variable that a run sets by hand reads as unset in the child conditions, as GitLab does not forward it without forward:pipeline_variables, but its value can reach the scripts of the child jobs")
	}
	if j.Matrix != nil {
		caveats = append(caveats, "the matrix of the call applies to every child job, so a job that needs a matrix job waits for all its instances")
	}
	if len(child.Finally) > 0 {
		caveats = append(caveats, "the finally jobs of the child run after every job of the calling pipeline")
	}
	for _, c := range caveats {
		detail += "; " + c
	}
	detail += "; a return to " + callSource(kind) + " restores the call and the child file only with the round-trip metadata"
	f.res.Entries = append(f.res.Entries, report.Entry{
		IRPath: path, Feature: callFeature(kind), Level: caps.Emulated, Strategy: EntryStrategy, Detail: detail,
	})
	f.res.Entries = append(f.res.Entries, f.dropped(j, path, kind, child, args)...)
}

// dropped gives each key of the call or of the child that no inlined job carries its own lost verdict.
func (f *flattener) dropped(j *ir.Job, path, kind string, child *ir.Pipeline, args *callInputs) []report.Entry {
	var out []report.Entry
	lost := func(feature caps.Feature, detail string) {
		out = append(out, report.Entry{IRPath: path, Feature: feature, Level: caps.Lost, Strategy: EntryStrategy, Detail: detail})
	}
	if child.Concurrency != nil {
		lost(caps.PipelineConcurrency, "the concurrency group of the child pipeline has no place in the calling pipeline")
	}
	if child.Permissions != nil {
		lost(caps.PipelinePermissions, "the permissions of the child pipeline have no place in the calling pipeline")
	}
	if keys := childExtKeys(child); len(keys) > 0 {
		lost(caps.ExtPassthrough, "the child pipeline keys "+strings.Join(keys, ", ")+" are not carried")
	}
	if keys := callExtKeys(j, kind); len(keys) > 0 {
		lost(caps.ExtPassthrough, "the call job keys "+strings.Join(keys, ", ")+" are not carried")
	}
	if j.Timeout != nil {
		lost(caps.JobTimeout, "the timeout of the call job does not bound the child jobs")
	}
	if j.Retry != nil {
		lost(caps.JobRetry, "the retry of the call job does not apply to the child jobs")
	}
	for _, u := range args.unused {
		lost(caps.PipelineInputs, "the input "+u+" is not declared by the called workflow")
	}
	return out
}

// childExtKeys lists the Ext keys of the child pipeline that the rewrite did not fold into a job.
func childExtKeys(child *ir.Pipeline) []string {
	var keys []string
	for dialect, v := range child.Ext {
		m, _ := v.(map[string]any)
		for k := range m {
			if !foldedPipelineKeys[k] {
				keys = append(keys, dialect+"."+k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// foldedPipelineKeys are the pipeline keys that already act through the jobs of the child.
var foldedPipelineKeys = map[string]bool{"workflow": true, "stages": true, "default": true, "hidden": true}

// callExtKeys lists the Ext keys of the call job that no inlined job carries.
func callExtKeys(j *ir.Job, kind string) []string {
	var keys []string
	for dialect, v := range j.Ext {
		m, _ := v.(map[string]any)
		for k := range m {
			switch {
			case strings.HasPrefix(k, "_"), k == extTrigger, k == "secrets", kind == kindGitLab && k == "stage":
			default:
				keys = append(keys, dialect+"."+k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// RemapPaths moves report entries about the child pipeline from to the paths that its jobs take once they run
// inline in the pipeline `to` as jobs <call>-<id>. An entry about the pipeline itself lands on the call job.
func RemapPaths(entries []report.Entry, from, to, call string) []report.Entry {
	out := make([]report.Entry, len(entries))
	copy(out, entries)
	root := "pipeline/" + from
	for i, e := range out {
		rest, ok := strings.CutPrefix(e.IRPath, root)
		if !ok || (rest != "" && rest[0] != '/') {
			continue
		}
		if job, ok := strings.CutPrefix(rest, "/job/"); ok {
			out[i].IRPath = jobPath(to, call+idJoin+job)
			continue
		}
		out[i].IRPath = jobPath(to, call) + rest
	}
	return out
}

func callSource(kind string) string {
	if kind == kindGitLab {
		return "GitLab"
	}
	return "GitHub Actions"
}

// hiding is what a GitLab child must not see of its parent: a variable that is not in seen reads as unset.
type hiding struct{ seen map[string]bool }

// hidden says what the child jobs of a GitLab trigger see. GitLab forwards the variables of the trigger job and
// the pipeline file only with forward:yaml_variables, and a variable that a run sets by hand only with
// forward:pipeline_variables. The result is nil when nothing is hidden.
func (f *flattener) hidden(j *ir.Job, child *ir.Pipeline, cj *ir.Job, fwd bool) *hiding {
	if callKind(j) == kindGitLab && forwardsPipelineVars(j) {
		return nil
	}
	h := &hiding{seen: map[string]bool{}}
	for _, vars := range [][]ir.Var{child.Env, cj.Env} {
		for _, v := range vars {
			h.seen[v.Name] = true
		}
	}
	if fwd && callKind(j) == kindGitLab {
		for _, vars := range [][]ir.Var{j.Env, f.pl.Env} {
			for _, v := range vars {
				h.seen[v.Name] = true
			}
		}
	}
	return h
}

func forwardsPipelineVars(j *ir.Job) bool {
	f, _ := trigger(j)[extForward].(map[string]any)
	v, _ := f[extPipelineVars].(bool)
	return v
}

// hiddenEnv sets each parent variable that the child must not see to the empty value, so the child reads it as unset.
func hiddenEnv(h *hiding, parent, own []ir.Var) []ir.Var {
	if h == nil {
		return nil
	}
	var out []ir.Var
	for _, v := range parent {
		if !h.seen[v.Name] && !slices.ContainsFunc(own, func(o ir.Var) bool { return o.Name == v.Name }) {
			out = append(out, ir.Var{Name: v.Name})
		}
	}
	return out
}

// unsetInScripts makes the script steps of j read each hidden variable as unset, not as empty: the empty value
// differs from unset for ${NAME-x}, ${NAME+x} and test -v, and a value that a run sets by hand must not reach the script.
func unsetInScripts(j *ir.Job, hidden []ir.Var) {
	if len(hidden) == 0 {
		return
	}
	names := make([]string, len(hidden))
	for i, v := range hidden {
		names[i] = v.Name
	}
	line := "unset -v " + strings.Join(names, " ")
	for _, st := range j.Steps {
		if st == nil || st.Action != nil || len(st.Script) == 0 || emitkit.IsFailLoud(st.Script) {
			continue
		}
		first, rest, multi := strings.Cut(st.Script[0], "\n")
		if strings.HasPrefix(first, "#!") && multi {
			st.Script[0] = first + "\n" + line + "\n" + rest
		} else if !strings.HasPrefix(first, "#!") {
			st.Script[0] = line + "\n" + st.Script[0]
		}
	}
}

// hideVars reads each variable that the child cannot see as unset in a condition.
func hideVars(e expr.Expr, h *hiding) expr.Expr {
	if h == nil {
		return e
	}
	return expr.FoldConst(expr.MapRefs(e, func(r expr.Ref) expr.Expr {
		if len(r.Path) == 2 && r.Path[0] == expr.PrefixEnv && !h.seen[r.Path[1]] {
			return expr.Lit{Kind: expr.LitNull}
		}
		return r
	}))
}
