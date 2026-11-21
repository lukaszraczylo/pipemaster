package tekton

import (
	"fmt"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const pacPrefix = "pipelinesascode.tekton.dev/"

// plCtx is the pipeline being read: the task names that references can point at.
type plCtx struct {
	p    *project
	k    *sink
	ids  map[string]string
	file string
	plID string
	// ws is true when a job restored the workspace marker.
	ws bool
}

func (c *plCtx) taskID(name string) (string, bool) {
	id, ok := c.ids[name]
	return id, ok
}

func (p *project) pipeline(src *pipeSrc) (*ir.Pipeline, error) {
	if src.spec == nil {
		return nil, fmt.Errorf("%s: Pipeline %q needs a spec", src.doc.loc(0), src.name)
	}
	id := src.name
	if m := valueOf2(src.meta, "name"); m != "" {
		id = m
	}
	if id == "" {
		id = fallbackPipelineID
	}
	pl := &ir.Pipeline{ID: id}
	k := newSink(p.rep, src.doc.file, "pipeline/"+id)
	r := newReader(src.spec)
	c := &plCtx{p: p, k: k, ids: map[string]string{}, file: src.doc.file, plID: id}
	if name, ok := textOf(r.take("displayName")); ok {
		pl.Name = name
	}
	if v, ok := r.take("description"); ok {
		k.vals["description"] = plain(v)
	}
	p.metadata(src, k)
	if err := p.params(pl, k, r); err != nil {
		return nil, err
	}
	pipeWS, hasWS := r.take("workspaces")
	if v, ok := r.take("results"); ok {
		k.keep("results", v, caps.ExtPassthrough, "pipeline results have no IR field; kept in Ext", r.line("results"))
	}
	tasks, ok := r.take("tasks")
	if !ok {
		return nil, fmt.Errorf("%s: Pipeline %q needs spec.tasks", src.doc.loc(0), id)
	}
	finally, _ := r.take("finally")
	k.keepRest(r)
	if err := c.jobs(pl, tasks, finally); err != nil {
		return nil, fmt.Errorf("%s: %w", src.doc.file, err)
	}
	if hasWS {
		if c.ws {
			pipeWS = withoutWorkspace(pipeWS)
		}
		if pipeWS != nil {
			k.keep("workspaces", pipeWS, caps.ExtPassthrough, "pipeline workspaces have no IR field; kept in Ext", r.line("workspaces"))
		}
	}
	pl.Ext = k.ext()
	return pl, nil
}

// valueOf2 reads the ID marker of a key, or of the mapping when key is empty.
func valueOf2(m *omap, key string) string {
	if m == nil {
		return ""
	}
	return m.marks[key]
}

// metadata keeps the metadata keys with no IR field, and the run that embeds a pipeline.
func (p *project) metadata(src *pipeSrc, k *sink) {
	if src.meta != nil {
		rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
		for _, key := range src.meta.keys {
			if key != "name" {
				rest.set(key, src.meta.vals[key], src.meta.lines[key])
			}
		}
		if len(rest.keys) > 0 {
			k.keep("metadata", rest, caps.ExtPassthrough, "metadata labels and annotations have no IR field; kept in Ext", src.meta.lines["labels"])
		}
		if ann, ok := asMap(src.meta.vals["annotations"]); ok && src.run != nil {
			for _, key := range ann.keys {
				if strings.HasPrefix(key, pacPrefix) {
					k.note(caps.TriggerPush, "Pipelines-as-Code annotation "+key+" has trigger semantics that the IR does not read; kept in Ext", ann.lines[key])
					break
				}
			}
		}
	}
	if src.run != nil {
		spec, _ := asMap(src.run.top.vals["spec"])
		run := &omap{vals: map[string]any{}, lines: map[string]int{}}
		if spec != nil {
			for _, key := range spec.keys {
				if key != "pipelineSpec" {
					run.set(key, spec.vals[key], spec.lines[key])
				}
			}
		}
		if len(run.keys) > 0 {
			k.keep("pipelineRun", run, caps.ExtPassthrough, "the PipelineRun spec beside pipelineSpec has no IR field; kept in Ext", src.run.line)
		}
	}
}

// params maps the pipeline params to inputs. A ctx-* param is the context vocabulary and maps to nothing.
func (p *project) params(pl *ir.Pipeline, k *sink, r *reader) error {
	v, ok := r.take("params")
	if !ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("spec.params must be a list")
	}
	extra := &omap{vals: map[string]any{}, lines: map[string]int{}}
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return fmt.Errorf("param %d must be a mapping", i+1)
		}
		name, _ := text(m.vals["name"])
		if name == "" {
			return fmt.Errorf("param %d needs a name", i+1)
		}
		if _, ctx := exprtekton.CtxRef(name); ctx {
			continue
		}
		in := ir.Input{Name: name}
		pr := newReader(m)
		pr.take("name")
		if t, ok := textOf(pr.take("type")); ok {
			in.Type = t
		}
		if t, ok := textOf(pr.take("description")); ok {
			in.Description = t
		}
		def, hasDef := pr.take("default")
		if t, ok := text(def); ok {
			in.Default = t
		} else if hasDef {
			pr.release("default")
		}
		in.Required = !hasDef
		if rest := pr.rest(); len(rest) > 0 {
			sub := m.subset(rest)
			extra.set(name, sub, m.lines[name])
			k.note(caps.PipelineInputs, "param "+name+": key "+strings.Join(rest, ", ")+" has no IR field; kept in Ext", m.lines[name])
		}
		pl.Inputs = append(pl.Inputs, in)
	}
	if len(extra.keys) > 0 {
		k.vals["params"] = plain(extra)
	}
	return nil
}
