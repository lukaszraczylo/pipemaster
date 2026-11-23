package concourse

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	globMeta   = "{}[]\\!"
	keyTimeout = "timeout"
	keyAttempt = "attempts"
	keyDo      = "do"
)

// hooks maps the job hooks that have a status function to that function, in the order the steps are written.
var hooks = []struct{ key, status string }{{"on_success", "success"}, {"on_failure", "failure"}, {"ensure", "always"}}

// stepHooks are the hook keys of a step.
var stepHooks = []string{"on_success", "on_failure", "on_error", "on_abort", "ensure"}

// builtJob is a parsed job with what the pipeline needs to know about it.
type builtJob struct {
	job *ir.Job
	// written maps a task step to the name that the file gives its task.
	written map[*ir.Step]string
	// name is the job name as the file writes it; hookDigest is the digest of its hooks.
	name, hookDigest string
	// roots lists the resources that a get without passed triggers on.
	roots []string
	// groups lists the concurrency groups of serial and serial_groups.
	groups []string
}

// jobBuilder is the state of one job.
type jobBuilder struct {
	b   *pipeBuilder
	job *ir.Job
	k   *sink
	ids map[string]int
	// taken holds the final IDs of the task steps read so far; a restored ID must not repeat one.
	taken map[string]bool
	// written maps each task step to its task name in the file.
	written map[*ir.Step]string
	// tried holds the steps whose carry said continue_on_error; a try in the file must still say it.
	tried map[*ir.Step]string
	path  string
	// caches holds the paths that every inline task of the plan caches; the job reads them as its cache.
	caches []string
}

func (b *pipeBuilder) job(m *omap) *builtJob {
	name, _ := text(m.Vals["name"])
	j := &ir.Job{ID: name}
	jb := &jobBuilder{b: b, job: j, ids: map[string]int{}, taken: map[string]bool{}, written: map[*ir.Step]string{}, path: b.path() + "/job/" + name}
	jb.k = newSink(b.rep, b.file, jb.path)
	b.present[jb.path] = true
	r := newReader(m)
	r.Take("name")
	bj := &builtJob{job: j, name: name, written: jb.written}
	hookText := map[string]any{}
	for _, h := range hooks {
		if v := m.Vals[h.key]; v != nil {
			hookText[h.key] = plainValue(v)
		}
	}
	bj.hookDigest = digestOf(hookText)
	plan, _ := r.Take(keyPlan)
	steps, _ := plan.([]any)
	jb.caches = commonCaches(steps)
	steps = jb.allowFailure(steps)
	steps = jb.unwrap(steps)
	gets := jb.leadingGets(&steps)
	steps, _ = jb.matrixMarker(steps)
	if co := jb.checkoutMarker(gets); co != nil {
		j.Steps = append(j.Steps, co)
	}
	for _, s := range steps {
		j.Steps = append(j.Steps, jb.step(s)...)
	}
	jb.ifMarker()
	if len(jb.caches) > 0 {
		j.Cache = []ir.Cache{{Paths: jb.caches}}
	}
	jb.hooks(r)
	b.collect(bj, plan, m)
	jb.serial(r, bj)
	jb.needs(bj, plan)
	jb.gateMarker(bj)
	jb.k.Out[ExtGets] = gets
	if len(gets) == 0 {
		jb.k.Out[ExtGets] = []any{}
	}
	jb.k.KeepRest(r)
	j.Ext = jb.k.Ext()
	jb.carryJob(m)
	jb.untried()
	return bj
}

// unwrap reads a plan that is one do step with only attempts and timeout beside it as the job body: the
// attempts become the job retry and the timeout the job timeout.
func (jb *jobBuilder) unwrap(plan []any) []any {
	if len(plan) != 1 {
		return plan
	}
	m, ok := asMap(plan[0])
	if !ok {
		return plan
	}
	body, isList := m.Vals[keyDo].([]any)
	if !isList {
		return plan
	}
	for _, key := range m.Order {
		if key != keyDo && key != keyAttempt && key != keyTimeout {
			return plan
		}
	}
	var attempts int
	var timeout *ir.Duration
	if t, has := m.Vals[keyAttempt]; has {
		n, ok := positiveInt(t)
		if !ok {
			return plan
		}
		attempts = n
	}
	if t, has := m.Vals[keyTimeout]; has {
		d, ok := parseDuration(t)
		if !ok {
			return plan
		}
		timeout = &d
	}
	if attempts > 0 {
		jb.job.Retry = &ir.Retry{Attempts: attempts}
	}
	jb.job.Timeout = timeout
	return body
}

// leadingGets takes the get steps at the start of the plan that read a git or time resource and returns their
// descriptors. They become the pipeline trigger and the job Ext, not steps.
func (jb *jobBuilder) leadingGets(steps *[]any) []any {
	var gets []any
	i := 0
	for ; i < len(*steps); i++ {
		m, ok := asMap((*steps)[i])
		if !ok {
			break
		}
		if g, isGet := jb.sourceGet(m); isGet {
			gets = append(gets, g)
			continue
		}
		if inner, ok := parallelGets(m); ok && len(inner) > 0 && jb.allSource(inner) {
			for _, g := range inner {
				gets = append(gets, jb.describeGet(g))
			}
			jb.k.Out[ExtGetsParallel] = true
			continue
		}
		break
	}
	*steps = (*steps)[i:]
	return gets
}

// sourceGet describes a plain get of a git or time resource.
func (jb *jobBuilder) sourceGet(m *omap) (any, bool) {
	if !isGet(m) || !jb.isSource(m) {
		return nil, false
	}
	return jb.describeGet(m), true
}

func isGet(m *omap) bool {
	_, has := m.Vals["get"]
	_, across := m.Vals["across"]
	return has && !across
}

func (jb *jobBuilder) isSource(m *omap) bool {
	r := jb.b.res[resourceName(m)]
	return jb.b.builtin(r, typeGit) || jb.b.builtin(r, typeTime)
}

func (jb *jobBuilder) allSource(l []*omap) bool {
	for _, g := range l {
		if !isGet(g) || !jb.isSource(g) {
			return false
		}
	}
	return true
}

// parallelGets reads an in_parallel step that holds only get steps.
func parallelGets(m *omap) ([]*omap, bool) {
	if len(m.Order) != 1 || m.Order[0] != "in_parallel" {
		return nil, false
	}
	v := m.Vals["in_parallel"]
	if cfg, ok := asMap(v); ok {
		if len(cfg.Order) != 1 || cfg.Order[0] != "steps" {
			return nil, false
		}
		v = cfg.Vals["steps"]
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	var out []*omap
	for _, e := range list {
		g, ok := asMap(e)
		if !ok {
			return nil, false
		}
		out = append(out, g)
	}
	return out, true
}

// describeGet keeps a source get verbatim and reports the keys the IR does not hold.
func (jb *jobBuilder) describeGet(m *omap) any {
	var kept []string
	for _, key := range m.Order {
		switch key {
		case "get", "resource", "passed", "trigger":
		default:
			kept = append(kept, key)
		}
	}
	name, _ := text(m.Vals["get"])
	detail := "get " + name + " is read as the job source and the pipeline trigger; it is kept in Ext"
	if len(kept) > 0 {
		detail = fmt.Sprintf("get %s: keys %s have no IR field; the get is kept in Ext", name, strings.Join(kept, ", "))
	}
	jb.k.Note(caps.ExtPassthrough, detail, m.Lines["get"])
	return plain(m)
}

func resourceName(m *omap) string {
	if r, ok := text(m.Vals["resource"]); ok {
		return r
	}
	n, _ := text(m.Vals["get"])
	if n == "" {
		n, _ = text(m.Vals["put"])
	}
	return n
}

func positiveInt(v any) (int, bool) {
	t, ok := text(v)
	if !ok {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(t, "%d", &n); err != nil || n <= 0 || fmt.Sprint(n) != t {
		return 0, false
	}
	return n, true
}

// serial reads serial and serial_groups into the concurrency groups; the keys stay in the job Ext.
func (jb *jobBuilder) serial(r *reader, bj *builtJob) {
	if on, _ := boolOf(r.M.Vals["serial"]); on {
		bj.groups = append(bj.groups, jb.job.ID)
	}
	if l, ok := stringList(r.M.Vals["serial_groups"]); ok {
		bj.groups = append(bj.groups, l...)
	}
}

// hooks appends the job hooks as steps with a status condition; on_error and on_abort stay in Ext.
func (jb *jobBuilder) hooks(r *reader) {
	for _, h := range hooks {
		v, ok := r.Take(h.key)
		if !ok {
			continue
		}
		steps := jb.hookSteps(v)
		for _, s := range steps {
			s.If = expr.Call{Name: h.status}
		}
		jb.job.Steps = append(jb.job.Steps, steps...)
		jb.emulated(caps.StepIf, "step with a status condition", fmt.Sprintf("job hook %s becomes a step with the condition %s()", h.key, h.status), r.Line(h.key))
	}
}

// hookSteps reads a hook: a do list becomes its steps, any other step is one step.
func (jb *jobBuilder) hookSteps(v any) []*ir.Step {
	if m, ok := asMap(v); ok && len(m.Order) == 1 && m.Order[0] == keyDo {
		if list, isList := m.Vals[keyDo].([]any); isList {
			var out []*ir.Step
			for _, s := range list {
				out = append(out, jb.step(s)...)
			}
			return out
		}
	}
	return jb.step(v)
}

// walk calls fn for every step map in v, descending into do, in_parallel and try, and into hooks when withHooks is set.
func walk(v any, withHooks bool, fn func(m *omap)) {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			walk(e, withHooks, fn)
		}
	case *omap:
		fn(t)
		walk(t.Vals[keyDo], withHooks, fn)
		walk(t.Vals["try"], withHooks, fn)
		ip := t.Vals["in_parallel"]
		if cfg, ok := asMap(ip); ok {
			ip = cfg.Vals["steps"]
		}
		walk(ip, withHooks, fn)
		if withHooks {
			for _, h := range stepHooks {
				walk(t.Vals[h], withHooks, fn)
			}
		}
	}
}

// collect records the resources the job uses, its trigger roots and whether it is manual.
func (b *pipeBuilder) collect(bj *builtJob, plan any, job *omap) {
	triggered := false
	seen := map[string]bool{}
	walkAll := func(v any, withHooks bool, plan bool) {
		walk(v, withHooks, func(m *omap) {
			if _, ok := m.Vals["get"]; ok || m.Vals["put"] != nil {
				b.used[resourceName(m)] = true
			}
			if !plan || m.Vals["get"] == nil {
				return
			}
			if on, _ := boolOf(m.Vals["trigger"]); !on {
				return
			}
			triggered = true
			if p, has := m.Vals["passed"]; has && !isEmptyList(p) {
				return
			}
			if n := resourceName(m); !seen[n] {
				seen[n] = true
				bj.roots = append(bj.roots, n)
			}
		})
	}
	walkAll(plan, false, true)
	walkAll(plan, true, false)
	for _, h := range stepHooks {
		walkAll(job.Vals[h], true, false)
	}
	bj.job.Manual = !triggered
}

func isEmptyList(v any) bool {
	l, ok := v.([]any)
	return ok && len(l) == 0
}

// needs maps every passed list in the plan to job needs. A name that does not resolve makes the job manual and is
// reported, so the job never starts before an upstream job it cannot name.
func (jb *jobBuilder) needs(bj *builtJob, plan any) {
	seen := map[string]bool{}
	walk(plan, false, func(m *omap) {
		if m.Vals["get"] == nil || m.Vals["passed"] == nil {
			return
		}
		list, ok := stringList(m.Vals["passed"])
		if !ok {
			jb.unresolved(bj, "passed is not a list of names", m.Lines["passed"])
			return
		}
		for _, p := range list {
			for _, id := range jb.resolve(bj, p, m.Lines["passed"]) {
				if !seen[id] {
					seen[id] = true
					bj.job.Needs = append(bj.job.Needs, ir.Need{JobID: id})
				}
			}
		}
	})
}

func (jb *jobBuilder) unresolved(bj *builtJob, why string, line int) {
	bj.job.Manual = true
	jb.k.Note(caps.JobNeeds, "passed "+why+"; the job is manual so that it never starts early, and the get is kept in Ext", line)
}

// resolve returns the job IDs that one passed entry names. A glob expands over the jobs of the pipeline.
func (jb *jobBuilder) resolve(bj *builtJob, p string, line int) []string {
	if !strings.ContainsAny(p, "*?") {
		if jb.b.jobSet[p] && p != jb.job.ID {
			return []string{p}
		}
		jb.unresolved(bj, "names "+p+", which is no other job of the pipeline", line)
		return nil
	}
	if strings.ContainsAny(p, globMeta) {
		jb.unresolved(bj, "uses the glob "+p+", which the parser cannot expand", line)
		return nil
	}
	var out []string
	for _, n := range jb.b.jobNames {
		if ok, err := path.Match(p, n); err == nil && ok && n != jb.job.ID && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		jb.unresolved(bj, "uses the glob "+p+", which matches no other job", line)
		return nil
	}
	jb.emulated(caps.JobNeeds, "glob expanded over the job names", fmt.Sprintf("the passed glob %s is expanded to the jobs %v", p, out), line)
	return out
}

// emulated adds an entry for a mapping that holds the behaviour in another form.
func (jb *jobBuilder) emulated(f caps.Feature, strategy, detail string, line int) {
	jb.k.Rep.Add(report.Entry{IRPath: jb.path, Feature: f, Level: caps.Emulated, Strategy: strategy, Detail: detail, SourceLoc: jb.k.Loc(line)})
}

// entry builds a report entry for the job.
func (jb *jobBuilder) entry(f caps.Feature, level caps.Level, strategy, detail string, line int) report.Entry {
	return report.Entry{IRPath: jb.path, Feature: f, Level: level, Strategy: strategy, Detail: detail, SourceLoc: jb.k.Loc(line)}
}

// commonCaches returns the cache paths that every inline task of the plan lists, in the same order. The emitter
// writes the job cache into each task, so a plan whose tasks differ keeps its caches in the tasks.
func commonCaches(plan []any) []string {
	var common []string
	seen := false
	same := true
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case *omap:
			if cfg, ok := asMap(x.Vals["config"]); ok && x.Vals["task"] != nil {
				paths, _ := cachePaths(cfg.Vals["caches"])
				if seen && !slices.Equal(paths, common) {
					same = false
				}
				common, seen = paths, true
			}
			for _, key := range x.Order {
				walk(x.Vals[key])
			}
		}
	}
	walk(plan)
	if !same {
		return nil
	}
	return common
}

// cachePaths reads a task caches list that has only path keys.
func cachePaths(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	var out []string
	for _, e := range list {
		m, ok := asMap(e)
		if !ok || len(m.Order) != 1 || m.Order[0] != "path" {
			return nil, false
		}
		p, ok := text(m.Vals["path"])
		if !ok || p == "" {
			return nil, false
		}
		out = append(out, p)
	}
	return out, true
}
