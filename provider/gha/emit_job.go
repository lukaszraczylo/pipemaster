package gha

import (
	"fmt"
	"math"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const defaultRunner = "ubuntu-latest"

// jobCtx tells jobNode how the job sits in its pipeline.
type jobCtx struct {
	pl       *ir.Pipeline
	mainIDs  []string
	finally  bool
	hasSteps bool
}

func jobPath(pid, jid string) string { return "pipeline/" + pid + "/job/" + jid }

// callStep returns the step of a job that is one reusable-workflow call, which GHA writes as job-level uses.
func callStep(j *ir.Job) *ir.Step {
	if len(j.Steps) == 1 && j.Steps[0] != nil && j.Steps[0].Kind == ir.StepPipelineCall && j.Steps[0].Action != nil {
		return j.Steps[0]
	}
	return nil
}

// minutesNode renders d as timeout-minutes so that the parser's float multiplication returns exactly d.
func minutesNode(d ir.Duration) *yaml.Node {
	f := float64(d) / float64(time.Minute)
	for i := 0; i < 8; i++ {
		if time.Duration(f*float64(time.Minute)) == time.Duration(d) {
			break
		}
		f = math.Nextafter(f, math.Inf(1))
	}
	if f == math.Trunc(f) && f < 1e9 {
		return intNode(int(f))
	}
	n, _ := plainNode(f)
	return n
}

func (e *emitter) jobNode(c jobCtx, j *ir.Job) (*yaml.Node, []string, error) {
	jp := jobPath(c.pl.ID, j.ID)
	ext := extMap(j.Ext, e.d.ID)
	e.foreignExt(jp, j.Ext)
	consumed := map[string]bool{}
	call := callStep(j)
	m := newMap()
	var markers []string

	name := j.Name
	if call != nil {
		name = call.Name
	}
	if name != "" {
		t, err := e.template(jp, name)
		if err != nil {
			return nil, nil, err
		}
		m.set("name", str(t))
	}
	if ids := e.needIDs(c, j); len(ids) > 0 {
		m.set("needs", strSeq(ids))
	}
	cond, mk := e.jobCondition(c, jp, j, call)
	markers = append(markers, mk...)
	m.set("if", cond)
	if call != nil {
		if err := e.callKeys(m, jp, call); err != nil {
			return nil, nil, err
		}
	} else if err := e.runsOn(m, jp, j, ext, consumed); err != nil {
		return nil, nil, err
	}
	for _, f := range []func(*omap, string, *ir.Job, map[string]any, map[string]bool) error{
		e.containerKey, e.servicesKey, e.strategyKey, e.environmentKey, e.limitKeys,
	} {
		if err := f(m, jp, j, ext, consumed); err != nil {
			return nil, nil, err
		}
	}
	if len(j.Env) > 0 {
		env, err := e.vars(jp, j.Env)
		if err != nil {
			return nil, nil, err
		}
		m.set("env", env)
	}
	if err := m.addExtra(ext, consumed); err != nil {
		return nil, nil, err
	}
	if call != nil {
		e.lostOnCall(jp, j)
	} else {
		steps, err := e.jobSteps(c, j)
		if err != nil {
			return nil, nil, err
		}
		m.set("steps", steps)
	}
	if err := e.outputsKey(m, jp, j); err != nil {
		return nil, nil, err
	}
	if c.finally {
		markers = append(markers, emitkit.Marker(caps.PipelineFinally, "pipeline/"+c.pl.ID))
		e.mark(caps.PipelineFinally, jp)
	}
	return m.n, append(markers, e.drain()...), nil
}

func (e *emitter) needIDs(c jobCtx, j *ir.Job) []string {
	var ids []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if c.finally {
		for _, id := range c.mainIDs {
			add(id)
		}
	}
	jp := jobPath(c.pl.ID, j.ID)
	for _, n := range j.Needs {
		if n.Optional {
			e.lose(jp, caps.JobNeeds, "optional need "+n.JobID+" has no GHA form; it is a hard dependency")
		}
		add(n.JobID)
	}
	return ids
}

// jobCondition composes the job's own if with the guards the finally and manual emulations add.
// A condition GHA cannot state disables the job rather than running it unguarded.
func (e *emitter) jobCondition(c jobCtx, jp string, j *ir.Job, call *ir.Step) (*yaml.Node, []string) {
	cond := j.If
	if cond != nil {
		t, ok := e.source(jp, caps.JobIf, cond)
		if ok {
			_, err := printExpr(t)
			ok = err == nil
		}
		if !ok {
			e.disable(jp, caps.JobIf, cond, "job")
			return boolNodeOf(false), e.manualMarker(jp, j)
		}
		cond = t
	}
	if call != nil && call.If != nil {
		sp := stepPath(jp, call.ID)
		t, ok := e.source(sp, caps.StepIf, call.If)
		if ok {
			_, err := printExpr(t)
			ok = err == nil
		}
		if !ok {
			e.disable(sp, caps.StepIf, call.If, "job")
			return boolNodeOf(false), e.manualMarker(jp, j)
		}
		cond = andExpr(cond, t)
	}
	var markers []string
	if j.Manual {
		guard := expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxEvent}}, R: expr.Lit{Kind: expr.LitStr, Str: "workflow_dispatch"}}
		cond = andExpr(cond, guard)
		markers = append(markers, emitkit.Marker(caps.JobManual, jp))
	}
	if c.finally {
		cond = andExpr(expr.Call{Name: "always"}, cond)
	}
	if cond == nil {
		return nil, markers
	}
	text, _ := printExpr(cond)
	return str(text), markers
}

// manualMarker tags a manual job whose condition is disabled, so Parse still reads the manual flag back.
func (e *emitter) manualMarker(jp string, j *ir.Job) []string {
	if !j.Manual {
		return nil
	}
	return []string{emitkit.Marker(caps.JobManual, jp)}
}

func andExpr(l, r expr.Expr) expr.Expr {
	switch {
	case l == nil:
		return r
	case r == nil:
		return l
	}
	return expr.Binary{Op: expr.OpAnd, L: l, R: r}
}

func (e *emitter) callKeys(m *omap, jp string, call *ir.Step) error {
	sp := stepPath(jp, call.ID)
	const why = "a reusable workflow call job accepts no such key"
	for _, l := range []struct {
		f  caps.Feature
		on bool
	}{
		{caps.StepEnv, len(call.Env) > 0}, {caps.StepTimeout, call.Timeout != nil},
		{caps.StepContinueOnError, call.ContinueOnError}, {caps.StepWorkdir, call.WorkDir != ""}, {caps.StepShell, call.Shell != ""},
	} {
		if l.on {
			e.lose(sp, l.f, why)
		}
	}
	m.set("uses", str(actionString(call.Action)))
	if len(call.With) > 0 {
		w, err := e.withNode(jp, call.With)
		if err != nil {
			return err
		}
		m.set("with", w)
	}
	if sec, ok := extMap(call.Ext, e.d.ID)["secrets"]; ok {
		n, err := plainNode(sec)
		if err != nil {
			return err
		}
		m.set("secrets", n)
	}
	return nil
}

// lostOnCall reports the emulations that need steps a reusable-workflow call job does not have.
func (e *emitter) lostOnCall(jp string, j *ir.Job) {
	const why = "a reusable workflow call has no steps to hold the emulation"
	for _, l := range []struct {
		f  caps.Feature
		on bool
	}{{caps.JobRetry, j.Retry != nil}, {caps.JobCache, len(j.Cache) > 0}, {caps.JobArtifacts, len(j.Artifacts) > 0}} {
		if l.on {
			e.lose(jp, l.f, why)
		}
	}
	for _, n := range j.Needs {
		if n.Artifacts {
			e.lose(jp, caps.JobNeedsArtifacts, why)
		}
	}
}

func (e *emitter) runsOn(m *omap, jp string, j *ir.Job, ext map[string]any, consumed map[string]bool) error {
	r := j.Runner
	if r.Platform != "" {
		e.lose(jp, caps.JobRunner, "runner platform "+r.Platform+" has no GHA form")
	}
	labels, err := e.templates(jp, r.Labels)
	if err != nil {
		return err
	}
	group := ""
	for _, k := range sortedKeys(anyMap(r.Selector)) {
		if k != "group" {
			e.lose(jp, caps.JobRunner, "runner selector "+k+" has no GHA form")
			continue
		}
		if group, err = e.template(jp, r.Selector[k]); err != nil {
			return err
		}
	}
	rest, _ := ext[extRunsOn].(map[string]any)
	switch {
	case group == "" && len(labels) == 0:
		if _, raw := ext[extRunsOn]; raw {
			return nil
		}
		e.emulate(jp, caps.JobRunner, "default runner label "+defaultRunner, "the pipeline named no runner")
		e.mark(caps.JobRunner, jp)
		m.set("runs-on", str(defaultRunner))
	case group == "" && len(rest) == 0 && len(labels) == 1:
		m.set("runs-on", str(labels[0]))
	case group == "" && len(rest) == 0:
		m.set("runs-on", strSeq(labels))
	default:
		ro := newMap()
		if group != "" {
			ro.set("group", str(group))
		}
		if len(labels) > 0 {
			ro.set("labels", strSeq(labels))
		}
		if err := ro.addExtra(rest, nil); err != nil {
			return err
		}
		m.set("runs-on", ro.n)
	}
	consumed[extRunsOn] = true
	return nil
}

func anyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (e *emitter) containerKey(m *omap, jp string, j *ir.Job, ext map[string]any, consumed map[string]bool) error {
	if j.Container == nil {
		return nil
	}
	if len(j.Container.Entrypoint) > 0 {
		e.lose(jp, caps.JobContainer, "container entrypoint has no GHA key")
	}
	ref, err := e.template(jp, j.Container.Ref)
	if err != nil {
		return err
	}
	n, err := imageNode(ref, ext[extContainer])
	if err != nil {
		return err
	}
	m.set("container", n)
	consumed[extContainer] = true
	return nil
}

// imageNode writes a bare image string, or an image mapping when other keys ride along.
func imageNode(ref string, rest any) (*yaml.Node, error) {
	r, _ := rest.(map[string]any)
	if len(r) == 0 {
		return str(ref), nil
	}
	im := newMap()
	im.set("image", str(ref))
	return im.n, im.addExtra(r, nil)
}

func (e *emitter) servicesKey(m *omap, jp string, j *ir.Job, ext map[string]any, consumed map[string]bool) error {
	if len(j.Services) == 0 {
		return nil
	}
	rests, _ := ext[extServices].(map[string]any)
	sm := newMap()
	for _, sv := range j.Services {
		if len(sv.Image.Entrypoint) > 0 {
			e.lose(jp, caps.JobServices, "service "+sv.Name+" entrypoint has no GHA key")
		}
		ref, err := e.template(jp, sv.Image.Ref)
		if err != nil {
			return err
		}
		rest, _ := rests[sv.Name].(map[string]any)
		n := newMap()
		n.set("image", str(ref))
		if len(sv.Env) > 0 {
			env, err := e.vars(jp, sv.Env)
			if err != nil {
				return err
			}
			n.set("env", env)
		}
		if len(sv.Ports) > 0 {
			n.set("ports", strSeq(sv.Ports))
		}
		if err := n.addExtra(rest, nil); err != nil {
			return err
		}
		sm.set(sv.Name, n.n)
	}
	m.set("services", sm.n)
	consumed[extServices] = true
	return nil
}

func (e *emitter) environmentKey(m *omap, jp string, j *ir.Job, ext map[string]any, consumed map[string]bool) error {
	if j.Environment == nil {
		return nil
	}
	name, err := e.template(jp, j.Environment.Name)
	if err != nil {
		return err
	}
	url, err := e.template(jp, j.Environment.URL)
	if err != nil {
		return err
	}
	rest, _ := ext[extEnvironment].(map[string]any)
	consumed[extEnvironment] = true
	if url == "" && len(rest) == 0 {
		m.set("environment", str(name))
		return nil
	}
	em := newMap()
	em.set("name", str(name))
	if url != "" {
		em.set("url", str(url))
	}
	m.set("environment", em.n)
	return em.addExtra(rest, nil)
}

func (e *emitter) limitKeys(m *omap, _ string, j *ir.Job, _ map[string]any, _ map[string]bool) error {
	if j.Timeout != nil {
		m.set("timeout-minutes", minutesNode(*j.Timeout))
	}
	if j.AllowFailure {
		m.set("continue-on-error", boolNodeOf(true))
	}
	return nil
}

func (e *emitter) outputsKey(m *omap, jp string, j *ir.Job) error {
	if len(j.Outputs) == 0 {
		return nil
	}
	om := newMap()
	for _, o := range j.Outputs {
		v, err := e.template(jp, o.Value)
		if err != nil {
			return err
		}
		om.set(o.Name, str(v))
	}
	m.set("outputs", om.n)
	return nil
}

func (e *emitter) withNode(path string, w map[string]ir.Value) (*yaml.Node, error) {
	m := newMap()
	keys := make([]string, 0, len(w))
	for k := range w {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n, err := e.value(path, w[k])
		if err != nil {
			return nil, err
		}
		if n != nil {
			m.set(k, n)
		}
	}
	return m.n, nil
}

func actionString(a *ir.ActionRef) string {
	switch a.Kind {
	case ir.ActionDocker:
		return dockerPrefix + a.Image
	case ir.ActionLocal:
		return a.Path
	}
	s := a.Owner + "/" + a.Repo
	if a.Path != "" {
		s += "/" + a.Path
	}
	return fmt.Sprintf("%s@%s", s, a.Ref)
}
