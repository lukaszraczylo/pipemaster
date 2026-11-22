package argo

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	annotationTitle = "workflows.argoproj.io/title"
	apiVersionKey   = "apiVersion"
)

// specSkip lists the pipeline Ext keys that the emitter writes through its own code.
var specSkip = map[string]bool{ExtResources: true, ExtTemplates: true, "params": true, "metadata": true, "arguments": true, "synchronization": true, "exitHook": true, "hooks": true}

// inputsArg maps each parameter that the emitter renamed to the name of the IR input, and holds the input types.
type inputsArg struct {
	Names map[string]string `json:"names,omitempty"`
	Types map[string]string `json:"types,omitempty"`
	// Required names the parameters of inputs with no default: a default of the parameter would hide it.
	Required []string `json:"required,omitempty"`
}

// concurrencyArg holds the cancel flag that a mutex cannot carry.
type concurrencyArg struct {
	Cancel bool `json:"cancel"`
}

// project writes every pipeline, then the triggers and the documents that an Argo file kept.
func (e *emitter) project(pls []*ir.Pipeline) error {
	e.planNames(pls)
	for _, pp := range e.plans {
		if err := e.pipelineDoc(pp); err != nil {
			return err
		}
	}
	if err := e.triggers(); err != nil {
		return err
	}
	e.keptResources()
	return nil
}

func (e *emitter) pipelineDoc(pp *plan) error {
	pl := pp.pl
	px := map[string]any{}
	if e.own {
		px = extOf(pl.Ext)
	}
	e.foreignExt(pp.path, pl.Ext)
	d := &outDoc{}
	pp.doc = d
	e.add(workflowsDir+pp.name+".yaml", d, pp.path)
	root := newMap()
	root.SetStr("apiVersion", apiVersion)
	root.SetStr("kind", kindTemplate)
	root.Set("metadata", e.metadata(pp, px))
	spec := newMap()
	spec.SetStr("entrypoint", entryTemplate)
	var tmpls []*yaml.Node
	var tasks, exits []*yaml.Node
	for _, jp := range pp.jobs {
		out, err := e.job(pp, jp)
		if err != nil {
			return err
		}
		if jp.finally {
			exits = append(exits, out.tasks...)
		} else {
			tasks = append(tasks, out.tasks...)
		}
		tmpls = append(tmpls, out.tmpls...)
	}
	e.markPipelineEnv(pl, pp.path)
	if len(tasks) == 0 {
		tasks, tmpls = e.placeholder(pp, tasks, tmpls)
	}
	spec.Set("arguments", e.arguments(pp, px))
	if pl.Concurrency != nil {
		spec.Set("synchronization", e.synchronization(pp, px))
	}
	if len(pp.claimOrder) > 0 {
		spec.Set("volumeClaimTemplates", e.claims(pp))
	}
	if len(exits) > 0 {
		hook := newMap()
		hook.SetStr("template", exitTemplate)
		if rest, ok := px["exitHook"].(map[string]any); ok {
			_ = hook.AddExtra(rest, map[string]bool{"template": true})
		}
		hooks := newMap()
		hooks.Set("exit", hook.N)
		if rest, ok := px["hooks"].(map[string]any); ok {
			_ = hooks.AddExtra(rest, nil)
		}
		spec.Set("hooks", hooks.N)
	}
	all := []*yaml.Node{dagTemplate(px, entryTemplate, "entrypoint_", tasks, true)}
	if len(exits) > 0 {
		all = append(all, dagTemplate(px, exitTemplate, "exit_", exits, false))
	}
	all = append(all, tmpls...)
	if l, ok := px[ExtTemplates].([]any); ok {
		for _, t := range l {
			all = append(all, e.node(pp.path, "templates", t))
		}
	}
	spec.Set("templates", seqOf(all...))
	skip := map[string]bool{}
	for k := range specSkip {
		skip[k] = true
	}
	rest := map[string]any{}
	for k, v := range px {
		if !skip[k] && !strings.HasPrefix(k, "entrypoint_") && !strings.HasPrefix(k, "exit_") && !strings.HasPrefix(k, "dag_") {
			rest[k] = v
		}
	}
	_ = spec.AddExtra(rest, nil)
	root.Set("spec", spec.N)
	d.Root = root.N
	e.made[kindTemplate+" "+pp.name] = true
	return nil
}

// metadata writes the name, the id comment, the title annotation and the metadata keys that an Argo file kept.
func (e *emitter) metadata(pp *plan, px map[string]any) *yaml.Node {
	m := newMap()
	m.SetStr("name", pp.name)
	if pp.name != pp.pl.ID {
		m.KeyComment("name", idComment(pp.pl.ID))
	}
	rest, _ := px["metadata"].(map[string]any)
	if pp.pl.Name != "" {
		ann := map[string]any{}
		if old, ok := rest["annotations"].(map[string]any); ok {
			for k, v := range old {
				ann[k] = v
			}
		}
		ann[annotationTitle] = pp.pl.Name
		cp := map[string]any{}
		for k, v := range rest {
			cp[k] = v
		}
		cp["annotations"] = ann
		rest = cp
	}
	_ = m.AddExtra(rest, nil)
	return m.N
}

// arguments writes the input parameters and the reserved context parameters.
func (e *emitter) arguments(pp *plan, px map[string]any) *yaml.Node {
	pl := pp.pl
	args := newMap()
	extra, _ := px["params"].(map[string]any)
	var params []*yaml.Node
	arg := inputsArg{Names: pp.renamed, Types: map[string]string{}}
	for _, in := range pl.Inputs {
		name := pp.inputs[in.Name]
		m := newMap()
		m.SetStr("name", name)
		if in.Default != "" || !in.Required {
			if unsafe, safe := tagKinds(in.Default); len(unsafe)+len(safe) > 0 {
				e.lose(pp.path, caps.PipelineInputs, "the default of input "+in.Name+" holds a tag, which Argo would substitute; the input has no default")
			} else {
				m.SetStr("value", in.Default)
			}
		}
		if in.Description != "" {
			m.SetStr("description", in.Description)
		}
		if rest, ok := extra[name].(map[string]any); ok {
			_ = m.AddExtra(rest, nil)
		}
		if in.Required {
			arg.Required = append(arg.Required, name)
		}
		if in.Type != "" {
			arg.Types[name] = in.Type
		}
		if in.Type != "" && in.Type != "string" {
			e.emulate(pp.path, caps.PipelineInputs, "string parameter", "input "+in.Name+" has type "+in.Type+", which Argo parameters lack; it is a string parameter and the type stays in the marker")
		}
		params = append(params, m.N)
		pp.args = append(pp.args, argSpec{name: name, value: in.Default, has: m.Has("value")})
	}
	for _, p := range ctxParams() {
		if pp.ctx[p] {
			params = append(params, nameValue(p, ""))
			pp.args = append(pp.args, argSpec{name: p, ctx: true})
		}
	}
	if len(params) > 0 {
		args.Set("parameters", seqOf(params...))
	}
	if rest, ok := px["arguments"].(map[string]any); ok {
		_ = args.AddExtra(rest, nil)
	}
	if len(arg.Names) > 0 || len(arg.Types) > 0 || len(arg.Required) > 0 {
		if err := e.markArg(caps.PipelineInputs, pp.path, arg); err != nil {
			e.lose(pp.path, caps.PipelineInputs, "the input marker cannot be written: "+err.Error())
		}
	}
	if args.Empty() {
		return nil
	}
	return args.N
}

func (e *emitter) synchronization(pp *plan, px map[string]any) *yaml.Node {
	c := pp.pl.Concurrency
	mx := newMap()
	mx.SetStr("name", c.Group)
	s := newMap()
	s.Set("mutexes", seqOf(mx.N))
	if rest, ok := px["synchronization"].(map[string]any); ok {
		_ = s.AddExtra(rest, nil)
	}
	if c.CancelInProgress {
		if err := e.markArg(caps.PipelineConcurrency, pp.path, concurrencyArg{Cancel: true}); err != nil {
			e.lose(pp.path, caps.PipelineConcurrency, "the cancel marker cannot be written: "+err.Error())
		}
	}
	e.emulate(pp.path, caps.PipelineConcurrency, "synchronization mutex", "a second run waits for the mutex"+cancelDetail(c))
	return s.N
}

func cancelDetail(c *ir.Concurrency) string {
	if c.CancelInProgress {
		return "; cancel-in-progress is lost, so a running Workflow is not cancelled"
	}
	return ""
}

func (e *emitter) claims(pp *plan) *yaml.Node {
	var out []*yaml.Node
	for _, name := range pp.claimOrder {
		meta := newMap()
		meta.SetStr("name", name)
		req := newMap()
		req.SetStr("storage", pp.claims[name].size)
		res := newMap()
		res.Set("requests", req.N)
		spec := newMap()
		spec.Set("accessModes", strSeq([]string{"ReadWriteOnce"}))
		spec.Set("resources", res.N)
		c := newMap()
		c.Set("metadata", meta.N)
		c.Set("spec", spec.N)
		out = append(out, c.N)
	}
	return seqOf(out...)
}

// dagTemplate writes a dag template of tasks. The Ext keys of the entry template come back with the prefix.
func dagTemplate(px map[string]any, name, prefix string, tasks []*yaml.Node, entry bool) *yaml.Node {
	t := newMap()
	t.SetStr("name", name)
	d := newMap()
	d.Set("tasks", seqOf(tasks...))
	if entry {
		dagRest := map[string]any{}
		for k, v := range px {
			if strings.HasPrefix(k, "dag_") {
				dagRest[strings.TrimPrefix(k, "dag_")] = v
			}
		}
		_ = d.AddExtra(dagRest, nil)
	}
	t.Set("dag", d.N)
	rest := map[string]any{}
	for k, v := range px {
		if strings.HasPrefix(k, prefix) {
			rest[strings.TrimPrefix(k, prefix)] = v
		}
	}
	_ = t.AddExtra(rest, nil)
	return t.N
}

var _ = ctxparam.Prefix

// placeholderArg names the task that stands in for a pipeline with no job.
type placeholderArg struct {
	Task string `json:"task"`
}

func (e *emitter) placeholder(pp *plan, tasks, tmpls []*yaml.Node) ([]*yaml.Node, []*yaml.Node) {
	e.note(pp.path, caps.StepScript, caps.Emulated, "placeholder task", "the pipeline has no job and an Argo DAG needs one task; a task that runs "+placeholderScript+" stands in")
	name := unique(pp.taskNames, placeholderTask, maxName)
	if err := e.markArg(caps.StepScript, pp.path, placeholderArg{Task: name}); err != nil {
		e.lose(pp.path, caps.StepScript, "the placeholder marker cannot be written: "+err.Error())
	}
	tmplName := unique(pp.tmpls, name, maxName)
	t := newMap()
	t.SetStr("name", name)
	t.SetStr("template", tmplName)
	c := newMap()
	c.SetStr("name", mainStep)
	c.SetStr("image", defaultImage)
	c.Set("command", strSeq([]string{shellCommand, shellFlags}))
	c.Set("args", seqOf(str(placeholderScript)))
	cs := newMap()
	cs.Set("containers", seqOf(c.N))
	tm := newMap()
	tm.SetStr("name", tmplName)
	tm.Set("containerSet", cs.N)
	return append(tasks, t.N), append(tmpls, tm.N)
}
