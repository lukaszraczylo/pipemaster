package tekton

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
)

// taskKeyPrefix marks an Ext key that belongs to the Task body, not to the PipelineTask.
const taskKeyPrefix = "task_"

// body reads a Task spec into the steps, container, env, services and outputs of the job. r is the reader of
// the spec when keys were already consumed from it.
func (b *jobBuilder) body(spec *omap, r *reader) error {
	if r == nil {
		r = newReader(spec)
	}
	if v, ok := r.Take("params"); ok {
		if err := b.taskParams(v, r.Line("params")); err != nil {
			return err
		}
	}
	if v, ok := r.Take("stepTemplate"); ok {
		if err := b.stepTemplate(v, r.Line("stepTemplate")); err != nil {
			return err
		}
	}
	sv, ok := r.Take("steps")
	if !ok {
		return fmt.Errorf("a Task needs steps")
	}
	if err := b.steps(sv); err != nil {
		return err
	}
	if v, ok := r.Take("sidecars"); ok {
		if err := b.sidecars(v); err != nil {
			return err
		}
	}
	if v, ok := r.Take("results"); ok {
		b.results(v, r.Line("results"))
	}
	if v, ok := r.Take("workspaces"); ok && b.ws == nil {
		b.k.Keep("task_workspaces", v, caps.ExtPassthrough, "Task workspaces have no IR field; kept in Ext", r.Line("workspaces"))
	}
	if v, ok := r.Take("description"); ok {
		b.k.Out["task_description"] = plain(v)
	}
	b.k.KeepRestAs(r, taskKeyPrefix)
	b.unused()
	return nil
}

// taskParams reads the Task params: a string default is substituted into the text, and any other shape
// stays in Ext.
func (b *jobBuilder) taskParams(v any, line int) error {
	list, ok := v.([]any)
	if !ok {
		return fmt.Errorf("params must be a list")
	}
	var kept []any
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return fmt.Errorf("a Task param needs a name")
		}
		r := newReader(m)
		r.Take("name")
		r.Take("type")
		if t, ok := text(m.Vals["type"]); ok && t != "string" {
			r.Release("type")
		}
		if d, ok := text(r.M.Vals["default"]); ok {
			r.Take("default")
			b.tx.defaults[name] = d
		}
		if rest := r.Rest(); len(rest) > 0 {
			kept = append(kept, plain(m))
			b.k.Note(caps.ExtPassthrough, "Task param "+name+": key "+rest[0]+" has no IR form; kept in Ext", line)
		} else if b.opaque[name] {
			kept = append(kept, plain(m))
		}
	}
	if len(kept) > 0 {
		b.k.Out["task_params"] = kept
	}
	return nil
}

// unused keeps the bound values that no text read.
func (b *jobBuilder) unused() {
	rest := map[string]any{}
	for name := range b.tx.bind {
		if !b.tx.used[name] {
			rest[name] = b.raw[name]
		}
	}
	if len(rest) > 0 {
		b.k.Out["unused_params"] = rest
		b.k.Note(caps.ExtPassthrough, "params that the Task does not read are kept in Ext", 0)
	}
}
