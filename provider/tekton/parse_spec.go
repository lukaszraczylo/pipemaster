package tekton

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
)

// body reads a Task spec into the steps, container, env, services and outputs of the job. r is the reader of
// the spec when keys were already consumed from it.
func (b *jobBuilder) body(spec *omap, r *reader) error {
	if r == nil {
		r = newReader(spec)
	}
	if v, ok := r.take("params"); ok {
		if err := b.taskParams(v, r.line("params")); err != nil {
			return err
		}
	}
	if v, ok := r.take("stepTemplate"); ok {
		if err := b.stepTemplate(v, r.line("stepTemplate")); err != nil {
			return err
		}
	}
	sv, ok := r.take("steps")
	if !ok {
		return fmt.Errorf("a Task needs steps")
	}
	if err := b.steps(sv); err != nil {
		return err
	}
	if v, ok := r.take("sidecars"); ok {
		if err := b.sidecars(v); err != nil {
			return err
		}
	}
	if v, ok := r.take("results"); ok {
		b.results(v, r.line("results"))
	}
	if v, ok := r.take("workspaces"); ok && b.ws == nil {
		b.k.keep("task_workspaces", v, caps.ExtPassthrough, "Task workspaces have no IR field; kept in Ext", r.line("workspaces"))
	}
	if v, ok := r.take("description"); ok {
		b.k.vals["task_description"] = plain(v)
	}
	b.k.keepRest(r)
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
		r.take("name")
		r.take("type")
		if t, ok := text(m.vals["type"]); ok && t != "string" {
			r.release("type")
		}
		if d, ok := text(r.m.vals["default"]); ok {
			r.take("default")
			b.tx.defaults[name] = d
		}
		if rest := r.rest(); len(rest) > 0 {
			kept = append(kept, plain(m))
			b.k.note(caps.ExtPassthrough, "Task param "+name+": key "+rest[0]+" has no IR form; kept in Ext", line)
		}
	}
	if len(kept) > 0 {
		b.k.vals["task_params"] = kept
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
		b.k.vals["unused_params"] = rest
		b.k.note(caps.ExtPassthrough, "params that the Task does not read are kept in Ext", 0)
	}
}
