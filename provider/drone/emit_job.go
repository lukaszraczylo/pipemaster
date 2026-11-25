package drone

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	keyKind     = "kind"
	keyType     = "type"
	keyName     = "name"
	keyClone    = "clone"
	keyServices = "services"
	keyNode     = "node"
)

// platformRE reads a label as the os/arch value of platform.
var platformRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+(/v[0-9]+)?$`)

// docCtx is the state of one pipeline document while it is written.
type docCtx struct {
	guard      expr.Expr
	changes    []expr.Expr
	pl         *ir.Pipeline
	j          *ir.Job
	plan       *docPlan
	uploads    map[string]bool
	written    map[string]string
	emul       *ir.Step
	depth      string
	name       string
	jp         string
	image      string
	secretVars []ir.Var
	held       []heldText
	pos        int
	took       int
	defImg     bool
}

// loseName reports a display name: the Drone pipeline name is its id.
func (e *emitter) loseName(path, name string) {
	if name != "" {
		e.lose(path, caps.DisplayName, "the Drone pipeline name is its id; the display name is kept in round-trip metadata")
	}
}

// document writes one planned document.
func (e *emitter) document(c *docCtx) (*yaml.Node, error) {
	j := c.j
	e.foreignExt(c.jp, j.Ext)
	e.loseName(c.jp, j.Name)
	e.cell, e.declared = c.plan.cellMap(), declaredNames(j.Matrix)
	ext := extOf(j.Ext)
	if j.Container != nil {
		c.image = j.Container.Ref
		e.jobEntrypoint(c)
	}
	cond, err := e.jobSet(c.pl, j, c.jp, c.plan.finally)
	if err != nil {
		return nil, err
	}
	if c.plan.never {
		cond = jobCond{set: disabledSet(), has: true}
	}
	c.guard = cond.guard
	c.changes = changesAtoms(c.guard, j.Steps)
	e.cellNotes(c)
	env, err := e.docEnv(c, ext)
	if err != nil {
		return nil, err
	}
	steps, err := e.steps(c)
	if err != nil {
		return nil, err
	}
	if j.Container != nil {
		if err := e.markArg(caps.JobContainer, c.jp, containerArg{Image: *j.Container, Taken: c.took}); err != nil {
			return nil, err
		}
	}
	if err := e.cellMarker(c); err != nil {
		return nil, err
	}
	m := newYMap()
	m.set(keyKind, ystr(kindPipeline))
	typ := typeDocker
	if t, ok := ext[keyType].(string); ok && t != "" {
		typ = t
	}
	m.set(keyType, ystr(typ))
	m.set(keyName, ystr(c.name))
	if err := e.platform(m, c, ext); err != nil {
		return nil, err
	}
	if err := e.docHeader(m, c, ext, cond, env); err != nil {
		return nil, err
	}
	m.set("steps", steps)
	svc, err := e.services(c, ext)
	if err != nil {
		return nil, err
	}
	m.set(keyServices, svc)
	if err := e.jobNotes(c); err != nil {
		return nil, err
	}
	skip := map[string]bool{keyTrigger: true, keyServices: true, keyNode: true, keyPlatform: true, keyEnv: true}
	if err := m.addExtra(ext, skip); err != nil {
		return nil, err
	}
	return m.n, nil
}

// cellNotes reports the matrix settings Drone cannot hold.
func (e *emitter) cellNotes(c *docCtx) {
	p := c.plan
	if c.j.Matrix == nil {
		return
	}
	if p.first {
		if c.j.Matrix.FailFast != nil {
			e.lose(c.jp, caps.JobMatrix, "fail-fast has no Drone form")
		}
		if c.j.Matrix.MaxParallel > 0 {
			e.lose(c.jp, caps.JobMatrix, "max-parallel has no Drone form")
		}
		if len(p.combos) == 0 {
			e.lose(c.jp, caps.JobMatrix, "the matrix has no combination; the document is written disabled")
		}
	}
}

// cellMarker writes the matrix marker of a cell document, after its steps, because the first cell holds their text.
func (e *emitter) cellMarker(c *docCtx) error {
	p := c.plan
	if c.j.Matrix == nil {
		return nil
	}
	arg := cellArg{Job: c.j.ID, Cell: p.cell, Index: p.index}
	if p.first {
		arg.Matrix, arg.Held = c.j.Matrix, c.held
		if env := slices.DeleteFunc(slices.Clone(c.j.Env), func(v ir.Var) bool { return !valueReadsMatrix(v.Value) }); len(env) > 0 {
			arg.Env = heldYAML(env)
		}
		for _, sv := range c.j.Services {
			if readsMatrix(sv.Image.Ref) {
				if arg.Services == nil {
					arg.Services = map[string]string{}
				}
				arg.Services[sv.Name] = sv.Image.Ref
			}
		}
	}
	return e.markArg(caps.JobMatrix, c.jp, arg)
}

func declaredNames(mx *ir.Matrix) map[string]bool {
	if mx == nil {
		return nil
	}
	out := map[string]bool{}
	for _, a := range mx.Axes {
		out[a.Name] = true
	}
	for _, cs := range [][]ir.Combo{mx.Include, mx.Exclude} {
		for _, c := range cs {
			for _, kv := range c {
				out[kv.Key] = true
			}
		}
	}
	return out
}

// docHeader sets the keys before the steps: clone, environment, trigger, depends_on.
func (e *emitter) docHeader(m *ymap, c *docCtx, ext map[string]any, cond jobCond, env *yaml.Node) error {
	if c.depth != "" {
		if _, has := ext[keyClone]; has {
			e.lose(c.jp, caps.StepAction, "clone depth "+c.depth+" is not written because the document already has a clone setting")
		} else {
			clone := newYMap()
			clone.set("depth", scalarOf(c.depth))
			m.set(keyClone, clone.n)
		}
	}
	if len(env.Content) > 0 {
		m.set(keyEnv, env)
	}
	if cond.has || extOf(c.j.Ext)[keyTrigger] != nil {
		extra, _ := ext[keyTrigger].(map[string]any)
		n, err := condNode(cond.set, extra)
		if err != nil {
			return err
		}
		m.set(keyTrigger, n)
	}
	m.set(keyDependsOn, e.dependsOn(c))
	return nil
}

// docEnv builds the document environment: the job variables, then the pipeline variables, as a map of strings.
// Drone reads only strings there, so a secret goes to every step instead.
func (e *emitter) docEnv(c *docCtx, ext map[string]any) (*yaml.Node, error) {
	env := newYMap()
	add := func(path string, vs []ir.Var) error {
		for _, v := range vs {
			if env.has(v.Name) {
				continue
			}
			if v.Value.Kind == ir.ValueSecret {
				c.secretVars = append(c.secretVars, v)
				continue
			}
			n, err := e.valueNode(path, v.Value)
			if err != nil {
				return err
			}
			env.set(v.Name, n)
		}
		return nil
	}
	if err := add(c.jp, c.j.Env); err != nil {
		return nil, err
	}
	if err := add("pipeline/"+c.pl.ID, c.pl.Env); err != nil {
		return nil, err
	}
	if err := env.mergeMap(ext[keyEnv]); err != nil {
		return nil, err
	}
	if len(c.secretVars) > 0 {
		e.emulate(c.jp, caps.JobEnv, "environment of every step", "a document environment holds strings only, so a secret variable is set on each step")
		if err := e.markVars(caps.JobEnv, c.jp, c.j.Env); err != nil {
			return nil, err
		}
	}
	if len(c.pl.Env) > 0 {
		if err := e.markVars(caps.PipelineEnv, "pipeline/"+c.pl.ID, c.pl.Env); err != nil {
			return nil, err
		}
	}
	if env.empty() {
		return ystrMapEmpty(), nil
	}
	return env.n, nil
}

// ystrMapEmpty is the empty environment: the document always has the key, as a map.
func ystrMapEmpty() *yaml.Node { return newYMap().n }

// dependsOn lists the documents this one waits for. A finally document waits for every other document of its pipeline.
func (e *emitter) dependsOn(c *docCtx) *yaml.Node {
	var names []string
	add := func(l []string) {
		for _, n := range l {
			if !slices.Contains(names, n) && n != c.name {
				names = append(names, n)
			}
		}
	}
	if c.plan.finally {
		for _, j := range c.pl.Jobs {
			if j != nil {
				add(c.plan.owner.namesOf(c.pl, j.ID))
			}
		}
	} else {
		for _, nd := range c.j.Needs {
			if nd.Optional {
				e.lose(c.jp, caps.JobNeeds, "Drone has no optional dependency; the need on "+nd.JobID+" is required")
			}
			add(c.plan.owner.namesOf(c.pl, nd.JobID))
		}
	}
	if c.plan.finally {
		if err := e.markArg(caps.PipelineFinally, "pipeline/"+c.pl.ID, finallyArg{Needs: c.j.Needs, NoIf: c.j.If == nil}); err != nil {
			e.comment("finally marker not carried: " + err.Error())
		}
	}
	if len(names) == 0 {
		return nil
	}
	return ystrSeq(names)
}

// platform writes platform for a label that has the os/arch form and node for the runner selector.
func (e *emitter) platform(m *ymap, c *docCtx, ext map[string]any) error {
	r := c.j.Runner
	var label string
	for _, l := range append([]string{r.Platform}, r.Labels...) {
		if l == "" || label != "" {
			continue
		}
		if platformRE.MatchString(l) || (c.plan.own && strings.Count(l, "/") == 1) {
			label = l
		}
	}
	n := newYMap()
	if label != "" {
		parts := strings.Split(label, "/")
		keys := []string{platformOS, platformArch, "variant"}
		for i, p := range parts {
			t, err := e.text(nil, c.jp, caps.JobRunner, valueText, p)
			if err != nil {
				return err
			}
			n.set(keys[i], ystr(t))
		}
	}
	if err := n.mergeMap(ext[keyPlatform]); err != nil {
		return err
	}
	m.set(keyPlatform, nonEmpty(n))
	sel := newYMap()
	for _, k := range sortedKeys(r.Selector) {
		sel.set(k, ystr(esc(r.Selector[k])))
	}
	if err := sel.mergeMap(ext[keyNode]); err != nil {
		return err
	}
	m.set(keyNode, nonEmpty(sel))
	return nil
}

func nonEmpty(m *ymap) *yaml.Node {
	if m.empty() {
		return nil
	}
	return m.n
}

// jobNotes writes the markers and reports of the job features that Drone has no key for.
func (e *emitter) jobNotes(c *docCtx) error {
	j, jp := c.j, c.jp
	if j.Timeout != nil {
		e.external(jp, caps.JobTimeout, "repo setting timeout", "set the repo timeout to "+time.Duration(*j.Timeout).String()+"; Drone has no per-pipeline or per-step timeout")
	}
	if len(j.Cache) > 0 {
		if err := e.markArg(caps.JobCache, jp, j.Cache); err != nil {
			return err
		}
	}
	if len(j.Artifacts) > 0 {
		if err := e.markArg(caps.JobArtifacts, jp, j.Artifacts); err != nil {
			return err
		}
	}
	if j.AllowFailure {
		e.mark(caps.JobAllowFailure, jp)
	}
	return nil
}

// jobEntrypoint reports a job container entrypoint that Drone cannot apply. Drone replaces the entrypoint of a
// command step with its script runner, so a step that inherits the container takes none. An empty entrypoint
// only clears the one of the image, which Drone already ignores for command steps.
func (e *emitter) jobEntrypoint(c *docCtx) {
	if !slices.ContainsFunc(c.j.Container.Entrypoint, func(s string) bool { return s != "" }) {
		return
	}
	if slices.ContainsFunc(c.j.Steps, func(st *ir.Step) bool { return st != nil && (st.Image == nil || st.Image.Ref == "") }) {
		e.lose(c.jp, caps.JobContainer, "Drone replaces the entrypoint of a command step with its script runner, so the job container entrypoint is not written")
	}
}

// services writes the job services as a list.
func (e *emitter) services(c *docCtx, ext map[string]any) (*yaml.Node, error) {
	if len(c.j.Services) == 0 {
		return nil, nil
	}
	rest, _ := ext[keyServices].(map[string]any)
	list := yseq()
	for _, sv := range c.j.Services {
		e.cut = false
		n, err := e.service(c.jp, sv, rest[sv.Name])
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", sv.Name, err)
		}
		if e.cut {
			e.cut = false
			e.lose(c.jp, caps.JobServices, "service "+sv.Name+" lost an expression in its image or entrypoint and is not written")
			e.comment("job.services " + c.jp + ": service " + sv.Name + " is not written")
			continue
		}
		list.Content = append(list.Content, n)
	}
	if len(list.Content) == 0 {
		return nil, nil
	}
	return list, nil
}

func (e *emitter) service(jp string, sv ir.Service, rest any) (*yaml.Node, error) {
	m := newYMap()
	m.set(keyName, ystr(sv.Name))
	img, err := e.text(nil, jp, caps.JobServices, valueText, sv.Image.Ref)
	if err != nil {
		return nil, err
	}
	m.set(keyImage, ystr(img))
	if len(sv.Image.Entrypoint) > 0 {
		ep := make([]string, len(sv.Image.Entrypoint))
		for i, s := range sv.Image.Entrypoint {
			if ep[i], err = e.text(nil, jp, caps.JobServices, valueText, s); err != nil {
				return nil, err
			}
		}
		m.set(keyEntrypoint, ystrSeq(ep))
	}
	env := newYMap()
	if err := e.envNode(jp, sv.Env, env); err != nil {
		return nil, err
	}
	rm, _ := rest.(map[string]any)
	if err := env.mergeMap(rm[keyEnv]); err != nil {
		return nil, err
	}
	if !env.empty() {
		m.set(keyEnv, env.n)
	}
	if len(sv.Ports) > 0 {
		m.set("ports", ystrSeq(escAll(sv.Ports)))
	}
	if rm != nil {
		return m.n, m.addExtra(rm, map[string]bool{keyEnv: true})
	}
	return m.n, nil
}
