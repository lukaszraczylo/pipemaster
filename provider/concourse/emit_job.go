package concourse

import (
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// jobSkip lists the job Ext keys that the emitter writes itself.
var jobSkip = map[string]bool{ExtGets: true, ExtGetsParallel: true, ExtTriggers: true}

// statusHooks maps a status function to the job hook that runs for it.
var statusHooks = map[string]string{"success": "on_success", "failure": "on_failure", "always": "ensure"}

// hookOrder is the order the hooks are written in.
var hookOrder = []string{"on_success", "on_failure", "ensure"}

// jobEmitter is the state of the job being written.
type jobEmitter struct {
	pe         *pipeEmitter
	j          *ir.Job
	mx         *matrixSpec
	checkout   *checkoutInfo
	taken      map[string]bool
	ext        map[string]any
	uploads    map[string]bool
	hooks      map[string][]*yaml.Node
	hookChains map[string]*chain
	guard      expr.Expr
	path       string
	mpath      string
	name       string
	img        string
	caches     []string
	// finallyTasks lists the written names of the tasks that stand for finally jobs in the hooks.
	finallyTasks []string
	actSeq     int
	manual     bool
	held       bool
}

// jobs writes every job of the pipeline.
func (pe *pipeEmitter) jobs() []*yaml.Node {
	e := pe.e
	taken := map[string]bool{}
	for _, j := range pe.pl.Jobs {
		name := unique(taken, adapter{}.SanitizeID(j.ID))
		pe.jobName[j.ID] = name
		if name != j.ID {
			e.emulate(pe.path+"/job/"+j.ID, caps.ExtPassthrough, "job name changed", "the job ID "+j.ID+" is not a Concourse identifier or collides after conversion; the job is "+name)
		}
	}
	needed := map[string]bool{}
	for _, j := range pe.pl.Jobs {
		for _, n := range j.Needs {
			needed[n.JobID] = true
		}
	}
	for _, j := range pe.pl.Jobs {
		if !needed[j.ID] {
			pe.sinks = append(pe.sinks, j)
		}
		if extOf(j.Ext)[serialKey] != nil || extOf(j.Ext)[serialGroupsKey] != nil {
			pe.serial = pe.serial || e.own
		}
	}
	pe.noteFinally()
	var out []*yaml.Node
	for _, j := range pe.pl.Jobs {
		out = append(out, pe.job(j))
	}
	return out
}

const (
	serialKey       = "serial"
	serialGroupsKey = "serial_groups"
)

// noteFinally reports where the finally jobs go: to the single sink job; a pipeline with no single sink loses them.
func (pe *pipeEmitter) noteFinally() {
	pl, e := pe.pl, pe.e
	if len(pl.Finally) == 0 {
		return
	}
	if len(pe.sinks) != 1 {
		for _, f := range pl.Finally {
			e.lose(pe.path+"/job/"+f.ID, caps.PipelineFinally, "the pipeline has no single sink job to hold the finally steps in an ensure hook; the job is not written")
		}
		e.lose(pe.path, caps.PipelineFinally, "the pipeline has no single sink job to hold the finally steps in an ensure hook")
		return
	}
	e.emulate(pe.path, caps.PipelineFinally, "ensure hook on the sink job", "the finally jobs run as hooks of the job "+pe.jobName[pe.sinks[0].ID]+", the only job that no other job needs")
}

// finallyFor lists the finally jobs that the job holds as hooks.
func (pe *pipeEmitter) finallyFor(j *ir.Job) []*ir.Job {
	if len(pe.sinks) != 1 || pe.sinks[0] != j {
		return nil
	}
	return pe.pl.Finally
}

// job writes one job.
func (pe *pipeEmitter) job(j *ir.Job) *yaml.Node {
	e := pe.e
	jx := &jobEmitter{
		pe: pe, j: j, taken: map[string]bool{}, uploads: map[string]bool{}, hooks: map[string][]*yaml.Node{},
		path: pe.path + "/job/" + j.ID, name: pe.jobName[j.ID],
	}
	jx.mpath = pe.mpath + "/job/" + jx.name
	if e.own {
		jx.ext = extOf(j.Ext)
	} else {
		e.foreignExt(jx.path, j.Ext)
	}
	for _, s := range pe.sources {
		jx.taken[s.name] = true
	}
	jx.meta()
	plan := jx.plan()
	jx.gateMarker()
	m := newMap()
	m.SetStr("name", jx.name)
	if c := pe.pl.Concurrency; c != nil && !pe.serial {
		m.Set(serialGroupsKey, strSeq([]string{c.Group}))
	}
	m.Set("plan", seqOf(plan...))
	hookText := map[string]any{}
	for _, h := range hookOrder {
		if nodes := jx.hooks[h]; len(nodes) > 0 {
			hn := hookNode(nodes)
			m.Set(h, hn)
			hookText[h] = nodeValue(hn)
		}
	}
	if len(jx.finallyTasks) > 0 {
		e.markArg(caps.PipelineFinally, pe.mpath, finallyArg{Job: jx.name, Tasks: jx.finallyTasks, Digest: digestOf(hookText)})
	}
	if jx.ext != nil {
		if err := m.AddExtra(jx.ext, jobSkip); err != nil {
			e.lose(jx.path, caps.ExtPassthrough, "the Ext of the job cannot be written: "+err.Error())
		}
	}
	jx.carryJob(m.N)
	return m.N
}

// hookNode is one step as it is, and several steps wrapped in do.
func hookNode(nodes []*yaml.Node) *yaml.Node {
	if len(nodes) == 1 {
		return nodes[0]
	}
	m := newMap()
	m.Set("do", seqOf(nodes...))
	return m.N
}

// meta writes the markers and entries that belong to the job as a whole and decides its condition.
func (jx *jobEmitter) meta() {
	e, j := jx.pe.e, jx.j
	if j.Name != "" || jx.name != j.ID {
		e.markArg(caps.DisplayName, jx.mpath, nameArg{Name: j.Name, ID: j.ID, Written: jx.name, Kind: "job"})
	}
	jx.manual = j.Manual || (j.Environment != nil && j.Environment.Approval)
	if j.Manual {
		e.emulate(jx.path, caps.JobManual, "job with no triggering get", "a person starts the job; Concourse has no approver identity")
	}
	if j.Environment != nil && j.Environment.Approval {
		e.emulate(jx.path, caps.JobEnvironmentApproval, "job with no triggering get", "a person starts the job; the reviewer list is lost")
	}
	if len(j.Runner.Labels) > 0 {
		e.emulate(jx.path, caps.JobRunner, "worker tags of the task", "the tasks carry the worker tags "+strings.Join(j.Runner.Labels, ", ")+"; a build waits until a worker has them")
	}
	if len(j.Cache) > 0 {
		jx.cachePaths()
	}
	jx.condition()
}

// gateMarker writes the gate of the job once the gets are known: the file holds a triggering get or it does not.
func (jx *jobEmitter) gateMarker() {
	j := jx.j
	if j.Manual || j.Environment != nil || jx.held {
		jx.pe.e.markArg(caps.JobManual, jx.mpath, gateArg{Manual: j.Manual, Environment: j.Environment, Held: jx.held})
	}
}

// condition decides how the job condition is written: a status function that always holds is dropped, a
// condition with a shell form guards every script task, and any other condition leaves the job with no triggering get.
func (jx *jobEmitter) condition() {
	e, j := jx.pe.e, jx.j
	if j.If == nil {
		return
	}
	if st, ok := statusOf(j.If); ok && (st == "always" || st == "success") {
		return
	}
	refs, ok := guardRefs(j.If)
	if !ok || !guardable(j.If) {
		jx.manual = true
		e.lose(jx.path, caps.JobIf, "the condition has no shell test form; the job has no triggering get, so a person starts it")
		return
	}
	probe := jx.newScope("")
	for _, r := range refs {
		env, spelled := probe.refVar(r)
		if want, named := exprconcourse.EnvName(r); !spelled || !named || env != want {
			jx.manual = true
			e.lose(jx.path, caps.JobIf, "the reference "+expr.String(r)+" has no spelling in a Concourse task; the job has no triggering get, so a person starts it")
			return
		}
	}
	jx.guard = j.If
	e.emulate(jx.path, caps.JobIf, "shell guard on each script task", "every script task of the job starts with the guard of the condition")
	e.markArg(caps.JobIf, jx.mpath, ifArg{If: expr.String(j.If)})
}

// cachePaths turns the job caches into task cache paths; a path that a task cannot cache is lost.
func (jx *jobEmitter) cachePaths() {
	e := jx.pe.e
	for _, c := range jx.j.Cache {
		for _, p := range c.Paths {
			clean := strings.TrimPrefix(p, "./")
			if clean == "" || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "~") || strings.Contains(clean, "..") || strings.Contains(clean, varOpen) || strings.Contains(clean, "${{") {
				e.lose(jx.path, caps.JobCache, "the cache path "+p+" is not a relative path inside the task directory, so a Concourse task cache cannot hold it")
				continue
			}
			if !slices.Contains(jx.caches, clean) {
				jx.caches = append(jx.caches, clean)
			}
		}
		if c.Key != "" || c.Policy != "" {
			e.emulate(jx.path, caps.JobCache, "task caches", "a task cache has no key or policy; the cache is kept per job and task")
		}
	}
}

// statusOf reads a bare status function.
func statusOf(e expr.Expr) (string, bool) {
	c, ok := e.(expr.Call)
	if !ok || len(c.Args) != 0 {
		return "", false
	}
	_, known := statusHooks[c.Name]
	return c.Name, known
}

// split separates the trailing steps whose condition is a bare status function: they run as job hooks.
func (jx *jobEmitter) split() (main []*ir.Step, hooks []*ir.Step) {
	steps := jx.j.Steps
	cut := len(steps)
	for cut > 0 {
		if _, ok := statusOf(ifOf(steps[cut-1])); !ok {
			break
		}
		cut--
	}
	return steps[:cut], steps[cut:]
}

func ifOf(st *ir.Step) expr.Expr {
	if st == nil {
		return nil
	}
	return st.If
}

// plan writes the gets, the steps, the hooks, and the wrappers that stand for a matrix, a timeout, retries and
// allow_failure.
func (jx *jobEmitter) plan() []*yaml.Node {
	e, j := jx.pe.e, jx.j
	main, hooks := jx.split()
	gets := jx.gets()
	jx.setupMatrix()
	ch := &chain{jx: jx}
	body := ch.steps(main)
	for _, st := range hooks {
		jx.hookStep(st)
	}
	for _, f := range jx.pe.finallyFor(j) {
		jx.finallyJob(f)
	}
	if jx.mx != nil && len(body) > 0 {
		body = []*yaml.Node{jx.mx.wrap(body)}
		e.emulate(jx.path, caps.JobMatrix, "across over the axes", "the tasks run once per cell inside one across step; the outputs of the cells are not visible after it")
		e.markArg(caps.JobMatrix, jx.mpath, matrixArg{Matrix: jx.mx.yaml, Across: nodeCanon(jx.mx.across)})
	} else if jx.mx != nil {
		e.lose(jx.path, caps.JobMatrix, "the job has no step that the matrix can repeat")
	}
	nodes := append(slices.Clone(gets), body...)
	return jx.wrap(nodes)
}

// wrap puts the plan in a do step that carries the job timeout and attempts, and in try for allow_failure.
func (jx *jobEmitter) wrap(nodes []*yaml.Node) []*yaml.Node {
	e, j := jx.pe.e, jx.j
	if j.Timeout == nil && j.Retry == nil && !j.AllowFailure {
		return nodes
	}
	do := newMap()
	do.Set("do", seqOf(nodes...))
	if j.Retry != nil && j.Retry.Attempts > 0 {
		do.Set("attempts", intNode(j.Retry.Attempts))
		e.emulate(jx.path, caps.JobRetry, "attempts on a do wrapper", "the whole plan is retried; attempts counts the total tries")
		if len(j.Retry.On) > 0 {
			e.emulate(jx.path, caps.JobRetry, "attempts on a do wrapper", "the retry conditions "+strings.Join(j.Retry.On, ", ")+" are lost; the plan retries on any failure")
		}
	}
	if j.Timeout != nil {
		do.SetStr("timeout", durationText(j.Timeout))
		e.emulate(jx.path, caps.JobTimeout, "timeout on a do wrapper", "the timeout covers the gets and the tasks")
	}
	if !j.AllowFailure {
		return []*yaml.Node{do.N}
	}
	e.emulate(jx.path, caps.JobAllowFailure, "try around the plan", "the job shows success when the plan fails")
	e.mark(caps.JobAllowFailure, jx.mpath)
	try := newMap()
	try.Set("try", do.N)
	return []*yaml.Node{try.N}
}

func durationText(d *ir.Duration) string { return time.Duration(*d).String() }

// finallyJob adds the steps of a finally job to the hook that its condition names.
func (jx *jobEmitter) finallyJob(f *ir.Job) {
	e := jx.pe.e
	fp := jx.pe.path + "/job/" + f.ID
	hook := "ensure"
	if f.If != nil {
		st, ok := statusOf(f.If)
		if !ok {
			e.lose(fp, caps.JobIf, "the condition of the finally job has no hook form; the job is not written")
			return
		}
		hook = statusHooks[st]
	}
	if !jx.pe.e.own {
		e.foreignExt(fp, f.Ext)
	}
	saved := jx.j
	jx.j = f
	jx.path = fp
	ch := &chain{jx: jx}
	nodes := ch.steps(f.Steps)
	jx.finallyTasks = append(jx.finallyTasks, taskNames(nodes)...)
	jx.j = saved
	jx.path = jx.pe.path + "/job/" + saved.ID
	jx.hooks[hook] = append(jx.hooks[hook], nodes...)
}

// hookStep writes one trailing status step as a task of its hook.
func (jx *jobEmitter) hookStep(st *ir.Step) {
	name, _ := statusOf(st.If)
	hook := statusHooks[name]
	cp := *st
	cp.If = nil
	ch := jx.hookChain(hook)
	jx.hooks[hook] = append(jx.hooks[hook], ch.step(&cp)...)
	jx.pe.e.emulate(jx.path+"/step/"+st.ID, caps.StepIf, "job hook "+hook, "the step runs as the "+hook+" hook of the job")
}

// hookChain returns the chain of a hook, so the second task of a hook reads the workspace of the first.
func (jx *jobEmitter) hookChain(hook string) *chain {
	if jx.hookChains == nil {
		jx.hookChains = map[string]*chain{}
	}
	if c, ok := jx.hookChains[hook]; ok {
		return c
	}
	c := &chain{jx: jx}
	jx.hookChains[hook] = c
	return c
}

// taskNames lists the names of the task steps in the nodes, at any depth.
func taskNames(nodes []*yaml.Node) []string {
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == "task" && n.Content[i+1].Kind == yaml.ScalarNode {
					out = append(out, n.Content[i+1].Value)
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return out
}
