package tekton

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

const pacPrefix = "pipelinesascode.tekton.dev/"

// plCtx is the pipeline being read: the task names that references can point at.
type plCtx struct {
	env    map[string]ir.Var
	p      *project
	k      *sink
	ids    map[string]string
	srcWS  map[string]bool
	file   string
	plID   string
	ws     bool
	envBad bool
}

func (c *plCtx) taskID(name string) (string, bool) {
	id, ok := c.ids[name]
	return id, ok
}

func (p *project) pipeline(src *pipeSrc) (*ir.Pipeline, error) {
	if src.spec == nil {
		return nil, fmt.Errorf("%s: Pipeline %q needs a spec", src.doc.Loc(0), src.name)
	}
	id := src.name
	if m := valueOf2(src.meta, "name"); m != "" {
		id = m
	}
	if id == "" {
		id = fallbackPipelineID
	}
	pl := &ir.Pipeline{ID: id}
	k := newSink(p.rep, src.doc.File, "pipeline/"+id)
	r := newReader(src.spec)
	c := &plCtx{p: p, k: k, ids: map[string]string{}, file: src.doc.File, plID: id}
	if name, ok := textOf(r.Take("displayName")); ok {
		pl.Name = name
	}
	if v, ok := r.Take("description"); ok {
		k.Out["description"] = plain(v)
	}
	p.metadata(src, k)
	renames, err := p.params(pl, k, r, readInputNames(src.doc.Head, id))
	if err != nil {
		return nil, err
	}
	pipeWS, hasWS := r.Take("workspaces")
	if v, ok := r.Take("results"); ok {
		k.Keep("results", v, caps.ExtPassthrough, "pipeline results have no IR field; kept in Ext", r.Line("results"))
	}
	tasks, ok := r.Take("tasks")
	if !ok {
		return nil, fmt.Errorf("%s: Pipeline %q needs spec.tasks", src.doc.Loc(0), id)
	}
	finally, _ := r.Take("finally")
	k.KeepRest(r)
	envVars := c.copiedEnv(src, id)
	if err := c.jobs(pl, tasks, finally); err != nil {
		return nil, fmt.Errorf("%s: %w", src.doc.File, err)
	}
	emitkit.RewriteInputRefs(pl, renames)
	if envVars != nil && !c.envBad {
		pl.Env = envVars
	}
	if hasWS {
		if c.ws {
			pipeWS = withoutWorkspace(pipeWS)
		}
		if pipeWS != nil {
			k.Keep("workspaces", pipeWS, caps.ExtPassthrough, "pipeline workspaces have no IR field; kept in Ext", r.Line("workspaces"))
		}
	}
	pl.Ext = k.Ext()
	return pl, nil
}

// valueOf2 reads the ID marker of a key, or of the mapping when key is empty.
func valueOf2(m *omap, key string) string {
	if m == nil {
		return ""
	}
	return m.Marks[key]
}

// metadata keeps the metadata keys with no IR field, and the run that embeds a pipeline.
func (p *project) metadata(src *pipeSrc, k *sink) {
	if src.meta != nil {
		rest := ynode.NewMap()
		for _, key := range src.meta.Order {
			if key != "name" {
				rest.Set(key, src.meta.Vals[key], src.meta.Lines[key])
			}
		}
		if len(rest.Order) > 0 {
			k.Keep("metadata", rest, caps.ExtPassthrough, "metadata labels and annotations have no IR field; kept in Ext", src.meta.Lines["labels"])
		}
		if ann, ok := asMap(src.meta.Vals["annotations"]); ok && src.run != nil {
			for _, key := range ann.Order {
				if strings.HasPrefix(key, pacPrefix) {
					k.Note(caps.TriggerPush, "Pipelines-as-Code annotation "+key+" has trigger semantics that the IR does not read; kept in Ext", ann.Lines[key])
					break
				}
			}
		}
	}
	if src.run != nil {
		spec, _ := asMap(src.run.Top.Vals["spec"])
		run := ynode.NewMap()
		if spec != nil {
			for _, key := range spec.Order {
				if key != "pipelineSpec" {
					run.Set(key, spec.Vals[key], spec.Lines[key])
				}
			}
		}
		if len(run.Order) > 0 {
			k.Keep("pipelineRun", run, caps.ExtPassthrough, "the PipelineRun spec beside pipelineSpec has no IR field; kept in Ext", src.run.Line)
		}
	}
}

// readInputNames reads the names that the emitter gave to inputs it had to rename.
func readInputNames(head, id string) map[string]string {
	var a inputsArg
	if !readMarks(head).arg(caps.PipelineInputs, "pipeline/"+id, &a) {
		return nil
	}
	return a.Names
}

// params maps the pipeline params to inputs. A ctx-* param is the context vocabulary and maps to nothing.
// names maps the params that the emitter renamed to their input names; the result holds the renames that apply.
func (p *project) params(pl *ir.Pipeline, k *sink, r *reader, names map[string]string) (map[string]string, error) {
	v, ok := r.Take("params")
	if !ok {
		p.staleInputNames(k, names, nil)
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("spec.params must be a list")
	}
	extra := ynode.NewMap()
	have := map[string]bool{}
	for _, e := range list {
		if m, ok := asMap(e); ok {
			n, _ := text(m.Vals["name"])
			have[n] = true
		}
	}
	renames := map[string]string{}
	for i, e := range list {
		m, ok := asMap(e)
		if !ok {
			return nil, fmt.Errorf("param %d must be a mapping", i+1)
		}
		name, _ := text(m.Vals["name"])
		if name == "" {
			return nil, fmt.Errorf("param %d needs a name", i+1)
		}
		if _, ctx := ctxparam.Ref(name); ctx {
			continue
		}
		in := ir.Input{Name: name}
		if orig, ok := names[name]; ok && !have[orig] {
			in.Name = orig
			renames[name] = orig
		}
		pr := newReader(m)
		pr.Take("name")
		if t, ok := textOf(pr.Take("type")); ok {
			in.Type = t
		}
		if t, ok := textOf(pr.Take("description")); ok {
			in.Description = t
		}
		def, hasDef := pr.Take("default")
		if t, ok := text(def); ok {
			in.Default = t
		} else if hasDef {
			pr.Release("default")
		}
		in.Required = !hasDef
		if rest := pr.Rest(); len(rest) > 0 {
			sub := m.Subset(rest)
			extra.Set(in.Name, sub, m.Lines[name])
			k.Note(caps.PipelineInputs, "param "+name+": key "+strings.Join(rest, ", ")+" has no IR field; kept in Ext", m.Lines[name])
		}
		pl.Inputs = append(pl.Inputs, in)
	}
	if len(extra.Order) > 0 {
		k.Out["params"] = plain(extra)
	}
	p.staleInputNames(k, names, renames)
	return renames, nil
}

// staleInputNames reports each renamed param that the file no longer holds as the marker says.
func (p *project) staleInputNames(k *sink, names, applied map[string]string) {
	for _, param := range sortedKeys(names) {
		if _, ok := applied[param]; !ok {
			p.rep.Add(report.Entry{
				IRPath: k.Path, Feature: caps.PipelineInputs, Level: caps.Lost, Strategy: "marker ignored",
				Detail: "marker ignored: the param " + param + " was edited after pipemaster wrote it; the file content is kept", SourceLoc: k.File,
			})
		}
	}
}

// copiedEnv reads the pipeline env that the marker holds; the jobs check it against their stepTemplate.
func (c *plCtx) copiedEnv(src *pipeSrc, id string) []ir.Var {
	var a envArg
	if !readMarks(src.doc.Head).arg(caps.PipelineEnv, "pipeline/"+id, &a) {
		return nil
	}
	var vars []ir.Var
	if yaml.Unmarshal([]byte(a.Env), &vars) != nil || len(vars) == 0 {
		return nil
	}
	c.env = map[string]ir.Var{}
	for _, v := range vars {
		c.env[v.Name] = v
	}
	return vars
}
