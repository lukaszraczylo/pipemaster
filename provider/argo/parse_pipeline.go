package argo

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
)

const (
	keyTemplates = "templates"
	hookExit     = "exit"
)

// plCtx is the workflow being read.
type plCtx struct {
	p       *project
	src     *pipeSrc
	k       *sink
	inNames map[string]string
	// restored holds the steps that a marker put back whole; they carry no copied env.
	restored map[*ir.Step]bool
	vols     map[string]volume
	binds    map[string][]string
	mounted  map[string][]string
	file     string
	plID     string
}

func (p *project) pipeline(src *pipeSrc) (*ir.Pipeline, error) {
	id := src.name
	if m := markOf(src.meta, "name"); m != "" {
		id = m
	}
	if id == "" {
		id = fallbackPipeline
	}
	pl := &ir.Pipeline{ID: id}
	k := newSink(p.rep, src.doc.File, "pipeline/"+id)
	c := &plCtx{p: p, src: src, k: k, file: src.doc.File, plID: id, restored: map[*ir.Step]bool{}, binds: map[string][]string{}, mounted: map[string][]string{}}
	r := newReader(src.spec)
	if err := p.params(pl, k, r); err != nil {
		return nil, fmt.Errorf("%s: %w", src.doc.Loc(0), err)
	}
	c.inputsMarker(pl)
	if v, ok := r.Take("synchronization"); ok {
		c.synchronization(pl, v, r.Line("synchronization"))
	}
	c.concurrencyMarker(pl)
	r.Take(keyTemplates)
	entry, err := c.entrypoint(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src.doc.Loc(0), err)
	}
	exit, hook := c.exitHandler(r)
	if pl.Jobs, err = c.orchestrate(entry, false, "entrypoint_"); err != nil {
		return nil, fmt.Errorf("%s: %w", src.doc.Loc(0), err)
	}
	c.refTypeMarker(pl)
	if exit != "" {
		if pl.Finally, err = c.orchestrate(exit, true, "exit_"); err != nil {
			return nil, fmt.Errorf("%s: %w", src.doc.Loc(0), err)
		}
		if hook != nil {
			k.Keep("exitHook", hook, caps.PipelineFinally, "the exit hook keys beside template have no IR field; kept in Ext", 0)
		}
	}
	c.keepWorkflowVolumes(r)
	c.linkVolumes(pl.Jobs)
	k.KeepRest(r)
	c.metadata(pl)
	c.unreached()
	pl.Ext = k.Ext()
	c.placeholderMarker(pl)
	c.staleNames(pl)
	c.pipelineEnvMarker(pl)
	c.staleGates(append(slices.Clone(pl.Jobs), pl.Finally...))
	return pl, nil
}

// markOf reads the ID marker of a key of a possibly nil mapping.
func markOf(m *omap, key string) string {
	if m == nil {
		return ""
	}
	return m.Marks[key]
}

// metadata reads the title annotation as the pipeline name and keeps the other metadata keys, which have no IR field.
func (c *plCtx) metadata(pl *ir.Pipeline) {
	if c.src.meta == nil {
		return
	}
	rest := metaRest(c.src.meta)
	if ann, ok := asMap(rest.Vals["annotations"]); ok {
		if title, has := text(ann.Vals[annotationTitle]); has {
			pl.Name = title
			var keep []string
			for _, k := range ann.Order {
				if k != annotationTitle {
					keep = append(keep, k)
				}
			}
			var keys []string
			for _, k := range rest.Order {
				if k != "annotations" || len(keep) > 0 {
					keys = append(keys, k)
				}
			}
			out := rest.Subset(keys)
			if len(keep) > 0 {
				out.Vals["annotations"] = ann.Subset(keep)
			}
			rest = out
		}
	}
	if len(rest.Order) > 0 {
		c.k.Keep("metadata", rest, caps.ExtPassthrough, "metadata has no IR field; kept in Ext", c.src.meta.Lines[rest.Order[0]])
	}
}

// unreached keeps the templates that nothing reads.
func (c *plCtx) unreached() {
	list, _ := valueOf(c.src.spec, keyTemplates).([]any)
	var out []any
	for _, e := range list {
		m, ok := asMap(e)
		name, named := text(valueOf(m, "name"))
		if ok && named && c.src.used[name] && c.src.tmpls[name] == m {
			continue
		}
		out = append(out, plain(e))
		label := "a template"
		if named {
			label = "template " + name
		}
		c.k.Note(caps.ExtPassthrough, label+" is not reached from the entrypoint, a job or the exit handler; kept in Ext", 0)
	}
	if len(out) > 0 {
		c.k.Out[ExtTemplates] = out
	}
}

// entrypoint names the template that starts the workflow.
func (c *plCtx) entrypoint(r *reader) (string, error) {
	if n, ok := textOf(r.Take("entrypoint")); ok {
		if c.src.tmpls[n] == nil {
			return "", fmt.Errorf("entrypoint %q names no template", n)
		}
		return n, nil
	}
	if n, ok := impliedEntry(c.src); ok {
		return n, nil
	}
	return "", fmt.Errorf("the workflow has no entrypoint")
}

// impliedEntry finds the entrypoint of a workflow that names none: the template main, or the only dag or steps template.
func impliedEntry(src *pipeSrc) (string, bool) {
	if src.tmpls["main"] != nil {
		return "main", true
	}
	var orchestrators []string
	for n, t := range src.tmpls {
		if kind, _ := templateKind(t); kind == kindDag || kind == kindSteps {
			orchestrators = append(orchestrators, n)
		}
	}
	if len(orchestrators) == 1 {
		return orchestrators[0], true
	}
	return "", false
}

// isLibrary tells a WorkflowTemplate or ClusterWorkflowTemplate that names no entrypoint and has none to infer, or an entrypoint that no template carries: other workflows call its templates.
func isLibrary(src *pipeSrc) bool {
	if src.cron != nil || (src.doc.Kind != kindTemplate && src.doc.Kind != kindClusterTmpl) {
		return false
	}
	if v, ok := src.spec.Vals["entrypoint"]; ok {
		n, named := text(v)
		return named && src.tmpls[n] == nil
	}
	_, ok := impliedEntry(src)
	return !ok
}

// exitHandler reads onExit or the exit hook; hook holds the keys of the exit hook beside its template.
func (c *plCtx) exitHandler(r *reader) (name string, hook any) {
	if n, ok := textOf(r.Take("onExit")); ok {
		return n, nil
	}
	hv, ok := r.Take("hooks")
	hm, isMap := asMap(hv)
	if !ok || !isMap || hm.Vals[hookExit] == nil {
		r.Release("hooks")
		return "", nil
	}
	em, _ := asMap(hm.Vals[hookExit])
	n, named := text(valueOf(em, "template"))
	if !named || c.src.tmpls[n] == nil {
		r.Release("hooks")
		return "", nil
	}
	if rest := em.Subset(restKeys(em, "template")); len(rest.Order) > 0 {
		hook = rest
	}
	if others := hm.Subset(restKeys(hm, hookExit)); len(others.Order) > 0 {
		c.k.Keep("hooks", others, caps.PipelineFinally, "hooks beside exit have no IR field; kept in Ext", r.Line("hooks"))
	}
	return n, hook
}

// restKeys lists the keys of m except those named.
func restKeys(m *omap, except ...string) []string {
	var out []string
	for _, k := range m.Order {
		skip := false
		for _, e := range except {
			skip = skip || k == e
		}
		if !skip {
			out = append(out, k)
		}
	}
	return out
}

// synchronization maps the first mutex to the concurrency group; the other lock keys stay in Ext.
func (c *plCtx) synchronization(pl *ir.Pipeline, v any, line int) {
	m, ok := asMap(v)
	if !ok {
		c.k.Keep("synchronization", v, caps.PipelineConcurrency, "synchronization is not a mapping; kept in Ext", line)
		return
	}
	rest := ynode.NewMap()
	for _, key := range m.Order {
		if key != "mutexes" {
			rest.Set(key, m.Vals[key], m.Lines[key])
		}
	}
	mutexes, _ := m.Vals["mutexes"].([]any)
	for i, e := range mutexes {
		mm, ok := asMap(e)
		name, named := text(valueOf(mm, "name"))
		if i == 0 && ok && named && len(mm.Order) == 1 {
			pl.Concurrency = &ir.Concurrency{Group: name}
			continue
		}
		rest.Order = append(rest.Order, "mutexes")
		list, _ := rest.Vals["mutexes"].([]any)
		rest.Vals["mutexes"] = append(list, e)
	}
	if len(rest.Order) > 0 {
		rest.Order = uniq(rest.Order)
		c.k.Keep("synchronization", rest, caps.PipelineConcurrency, "synchronization keys beside the first mutex name have no IR field (semaphores, other mutexes, namespace); kept in Ext", line)
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// params maps spec.arguments.parameters to inputs. A ctx-* param is the context vocabulary and maps to nothing.
func (p *project) params(pl *ir.Pipeline, k *sink, r *reader) error {
	av, ok := r.Take("arguments")
	am, isMap := asMap(av)
	if !ok || !isMap {
		return nil
	}
	rest := ynode.NewMap()
	for _, key := range am.Order {
		if key != "parameters" {
			rest.Set(key, am.Vals[key], am.Lines[key])
		}
	}
	if len(rest.Order) > 0 {
		k.Keep("arguments", rest, caps.PipelineInputs, "arguments keys beside parameters (artifacts) have no IR field; kept in Ext", r.Line("arguments"))
	}
	extra := ynode.NewMap()
	for i, e := range listOf(am.Vals["parameters"]) {
		m, ok := asMap(e)
		if !ok {
			return fmt.Errorf("argument %d must be a mapping", i+1)
		}
		name, _ := text(m.Vals["name"])
		if name == "" {
			return fmt.Errorf("argument %d needs a name", i+1)
		}
		if _, ctx := ctxparam.Ref(name); ctx {
			continue
		}
		in := ir.Input{Name: name}
		pr := newReader(m)
		pr.Take("name")
		if t, ok := textOf(pr.Take("description")); ok {
			in.Description = t
		}
		def, hasDef := pr.Take("value")
		if !hasDef {
			def, hasDef = pr.Take("default")
		}
		if t, ok := text(def); ok {
			in.Default = t
		} else if hasDef {
			pr.Release("value")
			pr.Release("default")
		}
		in.Required = !hasDef
		if restKeys := pr.Rest(); len(restKeys) > 0 {
			extra.Set(name, m.Subset(restKeys), m.Lines["name"])
			k.Note(caps.PipelineInputs, "param "+name+": key "+strings.Join(restKeys, ", ")+" has no IR field; kept in Ext", m.Lines["name"])
		}
		pl.Inputs = append(pl.Inputs, in)
	}
	if len(extra.Order) > 0 {
		k.Out["params"] = plain(extra)
	}
	return nil
}
