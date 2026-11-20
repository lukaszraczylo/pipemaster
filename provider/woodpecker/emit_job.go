package woodpecker

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	// defaultImage runs steps that name no image; the IR does not hold the runner image of a hosted runner.
	defaultImage = "ubuntu:latest"
	// defaultCloneImage is the clone plugin of v3.20.1 (shared/constant/constant.go); the linter requires an
	// image beside clone settings.
	defaultCloneImage = "docker.io/woodpeckerci/plugin-git:2.11.0"
	// maxAxisValues is the values-per-axis limit of the pinned schema.
	maxAxisValues = 10
	// maxMatrixInclude is the include limit of the pinned schema.
	maxMatrixInclude = 64

	keyWhen   = "when"
	keyMatrix = "matrix"
	keyLabels = "labels"
	keyInc    = "include"
)

// platformRE reads a label as the os/arch value of labels.platform.
var platformRE = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+(/v[0-9]+)?$`)

// wfCtx is the state of one workflow while its steps are written.
type wfCtx struct {
	pl      *ir.Pipeline
	j       *ir.Job
	names   map[string]string
	uploads map[string]bool
	jp      string
	name    string
	// image is what a step without its own image runs in; a setup action replaces it for the steps after it.
	image string
	// depth is the clone depth that an actions/checkout step asked for; empty means the default clone.
	depth string
	// entry is the job container entrypoint, which a step without its own image inherits.
	entry   []string
	finally bool
	defImg  bool
	// mapForm is true when the steps are written as a map keyed by name.
	mapForm bool
}

func (e *emitter) loseName(path, name, id string) {
	if name != "" && name != id {
		e.lose(path, caps.DisplayName, "the Woodpecker workflow name is its file name; the display name is kept in round-trip metadata")
	}
}

// workflow writes one job as a workflow file body.
func (e *emitter) workflow(c *wfCtx) (*yaml.Node, error) {
	j := c.j
	e.foreignExt(c.jp, j.Ext)
	e.loseName(c.jp, j.Name, j.ID)
	ext := extOf(j.Ext)
	if j.Container != nil {
		c.image = j.Container.Ref
		c.entry = j.Container.Entrypoint
	}
	steps, err := e.steps(c)
	if err != nil {
		return nil, err
	}
	m := newMap()
	if err := e.workflowHeader(m, c, ext); err != nil {
		return nil, err
	}
	if err := e.workflowBody(m, c, ext, steps); err != nil {
		return nil, err
	}
	skip := map[string]bool{keyWhen: true, keyMatrix: true, keyLabels: true, "services": true}
	if err := m.addExtra(ext, skip); err != nil {
		return nil, err
	}
	return m.n, nil
}

// workflowHeader sets the keys before the steps: when, matrix, labels, depends_on, concurrency.
func (e *emitter) workflowHeader(m *wmap, c *wfCtx, ext map[string]any) error {
	if raw, ok := ext[keyWhen]; ok {
		n, err := plainNode(raw)
		if err != nil {
			return err
		}
		m.set(keyWhen, n)
	} else {
		m.set(keyWhen, whenNode(completeEvents(e.jobWhen(c.pl, c.j, c.jp, c.finally))))
	}
	if c.j.Matrix != nil {
		n, err := e.matrix(c.jp, c.j.Matrix, ext)
		if err != nil {
			return err
		}
		m.set(keyMatrix, n)
	}
	if err := e.labels(m, c, ext); err != nil {
		return err
	}
	m.set("depends_on", e.dependsOn(c))
	return e.concurrency(m, c)
}

func (e *emitter) workflowBody(m *wmap, c *wfCtx, ext map[string]any, steps *yaml.Node) error {
	if c.depth != "" {
		if _, has := ext["clone"]; has {
			e.lose(c.jp, caps.StepAction, "clone depth "+c.depth+" is not written because the workflow already has a clone setting")
		} else {
			git := newMap()
			git.set(imageKey, str(defaultCloneImage))
			settings := newMap()
			settings.set("depth", scalarOf(c.depth))
			git.set("settings", settings.n)
			clone := newMap()
			clone.set("git", git.n)
			m.set("clone", clone.n)
		}
	}
	m.set("steps", steps)
	svc, err := e.services(c, ext)
	if err != nil {
		return err
	}
	m.set("services", svc)
	e.jobNotes(c)
	return nil
}

// jobNotes writes the markers and reports of the job features that Woodpecker has no key for.
func (e *emitter) jobNotes(c *wfCtx) {
	j, jp := c.j, c.jp
	if j.Timeout != nil {
		e.external(jp, caps.JobTimeout, "repo setting Timeout", "set the repo timeout to "+time.Duration(*j.Timeout).String()+"; Woodpecker limits the whole pipeline")
	}
	for _, f := range []struct {
		feature caps.Feature
		used    bool
	}{
		{caps.JobCache, len(j.Cache) > 0}, {caps.JobArtifacts, len(j.Artifacts) > 0}, {caps.JobAllowFailure, j.AllowFailure},
		{caps.JobEnv, len(j.Env) > 0},
	} {
		if f.used {
			e.mark(f.feature, jp)
		}
	}
	if len(c.pl.Env) > 0 {
		e.mark(caps.PipelineEnv, "pipeline/"+c.pl.ID)
	}
	if c.finally {
		e.mark(caps.PipelineFinally, "pipeline/"+c.pl.ID)
	}
}

// dependsOn lists the workflows this one waits for. A finally workflow waits for every other workflow of its
// pipeline and takes each as optional, so a workflow that a when filter skipped does not block it.
func (e *emitter) dependsOn(c *wfCtx) *yaml.Node {
	n := seqOf()
	add := func(name string, optional bool) {
		if !optional {
			n.Content = append(n.Content, str(name))
			return
		}
		m := newMap()
		m.set("name", str(name))
		m.set("optional", boolNode(true))
		n.Content = append(n.Content, m.n)
	}
	for _, nd := range c.j.Needs {
		if name, ok := c.names[nd.JobID]; ok {
			add(name, nd.Optional)
		}
	}
	if c.finally {
		for _, j := range c.pl.Jobs {
			if j != nil {
				add(c.names[j.ID], true)
			}
		}
	}
	if len(n.Content) == 0 {
		return nil
	}
	return n
}

// labels writes labels.platform for a label that has the os/arch form; every other runner label is lost.
func (e *emitter) labels(m *wmap, c *wfCtx, ext map[string]any) error {
	r := c.j.Runner
	var platform string
	for _, l := range append([]string{r.Platform}, r.Labels...) {
		if l == "" || platform != "" {
			continue
		}
		if e.own || platformRE.MatchString(l) {
			platform = l
		}
	}
	n := newMap()
	if platform != "" {
		v, err := e.text(nil, c.jp, caps.JobRunner, valueText, platform)
		if err != nil {
			return err
		}
		n.set("platform", str(v))
	}
	if err := n.mergeMap(ext[keyLabels]); err != nil {
		return err
	}
	if !n.empty() {
		m.set(keyLabels, n.n)
	}
	return nil
}

func (e *emitter) concurrency(m *wmap, c *wfCtx) error {
	cc := c.pl.Concurrency
	if cc == nil {
		return nil
	}
	n := newMap()
	n.set("limit", intNode(1))
	if cc.Group != "" {
		g, ok, err := e.groupText(c, cc.Group)
		if err != nil {
			return err
		}
		if ok {
			n.set("group", str(g))
		}
	}
	if cc.CancelInProgress {
		e.lose("pipeline/"+c.pl.ID, caps.PipelineConcurrency, "cancel-in-progress has no Woodpecker form; a limit queues runs")
	}
	m.set("concurrency", n.n)
	return nil
}

// groupText converts a concurrency group; a group with an expression Woodpecker cannot spell is not written,
// because a partial group would limit unrelated runs together.
func (e *emitter) groupText(c *wfCtx, g string) (string, bool, error) {
	probe := &emitter{led: e.led, from: e.from, own: e.own}
	out, err := probe.text(nil, "pipeline/"+c.pl.ID, caps.PipelineConcurrency, valueText, g)
	if err != nil {
		return "", false, err
	}
	if len(probe.head) > 0 {
		e.lose("pipeline/"+c.pl.ID, caps.PipelineConcurrency, "the group "+g+" holds an expression with no Woodpecker spelling; the group defaults to the workflow name")
		e.comment("pipeline.concurrency pipeline/" + c.pl.ID + ": group " + g)
		return "", false, nil
	}
	return out, true, nil
}

// matrix writes the matrix. Woodpecker reads only include when it is present, so axes beside include or
// exclude are expanded to explicit entries.
func (e *emitter) matrix(jp string, mx *ir.Matrix, ext map[string]any) (*yaml.Node, error) {
	if mx.FailFast != nil {
		e.lose(jp, caps.JobMatrix, "fail-fast has no Woodpecker form")
	}
	if mx.MaxParallel > 0 {
		e.lose(jp, caps.JobMatrix, "max-parallel has no Woodpecker form")
	}
	m := newMap()
	switch {
	case len(mx.Exclude) == 0 && (len(mx.Include) == 0 || len(mx.Axes) == 0) && !longAxis(mx):
		for _, a := range mx.Axes {
			vs, err := e.matrixValues(jp, a.Values)
			if err != nil {
				return nil, err
			}
			m.set(a.Name, strSeq(vs))
		}
		if len(mx.Include) > 0 {
			inc, err := e.comboList(jp, mx.Include)
			if err != nil {
				return nil, err
			}
			m.set(keyInc, inc)
		}
		if err := m.mergeMap(ext[keyMatrix]); err != nil {
			return nil, err
		}
	default:
		combos := expandMatrix(mx)
		if len(combos) > maxMatrixInclude {
			e.lose(jp, caps.JobMatrix, "the matrix has "+strconv.Itoa(len(combos))+" combinations; Woodpecker allows "+strconv.Itoa(maxMatrixInclude)+" include entries, so the first "+strconv.Itoa(maxMatrixInclude)+" are kept")
			combos = combos[:maxMatrixInclude]
		}
		inc, err := e.comboList(jp, combos)
		if err != nil {
			return nil, err
		}
		m.set(keyInc, inc)
		e.emulate(jp, caps.JobMatrix, "explicit include entries", "axes beside include or exclude, and axes of more than 10 values, are expanded into explicit entries")
		e.mark(caps.JobMatrix, jp)
	}
	return m.n, nil
}

func (e *emitter) matrixValues(jp string, vs []string) ([]string, error) {
	out := make([]string, len(vs))
	for i, v := range vs {
		t, err := e.text(nil, jp, caps.JobMatrix, valueText, v)
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}

func (e *emitter) comboList(jp string, cs []ir.Combo) (*yaml.Node, error) {
	n := seqOf()
	for _, c := range cs {
		m := newMap()
		for _, kv := range c {
			t, err := e.text(nil, jp, caps.JobMatrix, valueText, kv.Value)
			if err != nil {
				return nil, err
			}
			m.set(kv.Key, str(t))
		}
		n.Content = append(n.Content, m.n)
	}
	return n, nil
}

// expandMatrix lists the cartesian product of the axes without the excluded combinations, then the include entries.
func expandMatrix(mx *ir.Matrix) []ir.Combo {
	var out []ir.Combo
	if len(mx.Axes) > 0 {
		out = []ir.Combo{{}}
		for _, a := range mx.Axes {
			var next []ir.Combo
			for _, base := range out {
				for _, v := range a.Values {
					next = append(next, append(slices.Clone(base), ir.KV{Key: a.Name, Value: v}))
				}
			}
			out = next
		}
	}
	out = slices.DeleteFunc(out, func(c ir.Combo) bool {
		return slices.ContainsFunc(mx.Exclude, func(x ir.Combo) bool { return comboHolds(c, x) })
	})
	for _, c := range out {
		sort.Slice(c, func(i, j int) bool { return c[i].Key < c[j].Key })
	}
	return append(out, mx.Include...)
}

// comboHolds reports whether every pair of part appears in c.
func comboHolds(c, part ir.Combo) bool {
	for _, kv := range part {
		if !slices.Contains(c, kv) {
			return false
		}
	}
	return true
}

// services writes the job services as a map keyed by name.
func (e *emitter) services(c *wfCtx, ext map[string]any) (*yaml.Node, error) {
	if len(c.j.Services) == 0 {
		return nil, nil
	}
	rest, _ := ext["services"].(map[string]any)
	m := newMap()
	for _, sv := range c.j.Services {
		n, err := e.service(c.jp, sv, rest[sv.Name])
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", sv.Name, err)
		}
		m.set(sv.Name, n)
	}
	return m.n, nil
}

func (e *emitter) service(jp string, sv ir.Service, rest any) (*yaml.Node, error) {
	m := newMap()
	img, err := e.text(nil, jp, caps.JobServices, valueText, sv.Image.Ref)
	if err != nil {
		return nil, err
	}
	m.set("image", str(img))
	if len(sv.Image.Entrypoint) > 0 {
		ep, err := e.entrypoint(jp, sv.Image.Entrypoint)
		if err != nil {
			return nil, err
		}
		m.set("entrypoint", ep)
	}
	env := newMap()
	if err := e.envNode(jp, sv.Env, env); err != nil {
		return nil, err
	}
	if rm, ok := rest.(map[string]any); ok {
		if err := env.mergeMap(rm["environment"]); err != nil {
			return nil, err
		}
	}
	if !env.empty() {
		m.set("environment", env.n)
	}
	if len(sv.Ports) > 0 {
		m.set("ports", strSeq(sv.Ports))
	}
	if rm, ok := rest.(map[string]any); ok {
		return m.n, m.addExtra(rm, map[string]bool{"environment": true})
	}
	return m.n, nil
}

func (e *emitter) entrypoint(jp string, ep []string) (*yaml.Node, error) {
	out := make([]string, len(ep))
	for i, s := range ep {
		t, err := e.text(nil, jp, caps.StepImage, valueText, s)
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return strSeq(out), nil
}

func longAxis(mx *ir.Matrix) bool {
	return slices.ContainsFunc(mx.Axes, func(a ir.Axis) bool { return len(a.Values) > maxAxisValues })
}
