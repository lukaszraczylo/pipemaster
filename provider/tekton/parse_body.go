package tekton

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// kindTaskName is the only taskRef kind that names a Task of the parsed files.
const kindTaskName = "Task"

// taskBody reads the Task that the PipelineTask runs: an inline taskSpec, a Task of the parsed files, or a
// remote Task that stays a reference.
func (b *jobBuilder) taskBody(r *reader) error {
	ref, hasRef := r.take("taskRef")
	spec, hasSpec := r.take("taskSpec")
	switch {
	case hasSpec:
		sm, ok := asMap(spec)
		if !ok {
			return fmt.Errorf("taskSpec must be a mapping")
		}
		return b.inline(sm)
	case hasRef:
		rm, ok := asMap(ref)
		if !ok {
			return fmt.Errorf("taskRef must be a mapping")
		}
		return b.reference(rm, r.line("taskRef"))
	}
	if r.m.vals["pipelineRef"] != nil || r.m.vals["pipelineSpec"] != nil {
		b.k.note(caps.ExtPassthrough, "a task that runs a pipeline has no IR form; the keys stay in Ext", r.line("pipelineRef")+r.line("pipelineSpec"))
		return nil
	}
	return fmt.Errorf("needs taskRef or taskSpec")
}

// inline reads a taskSpec: the Task fields sit beside an optional metadata key.
func (b *jobBuilder) inline(sm *omap) error {
	r := newReader(sm)
	if v, ok := r.take("metadata"); ok {
		b.k.keep("task_metadata", v, caps.ExtPassthrough, "taskSpec metadata has no IR field; kept in Ext", r.line("metadata"))
	}
	if v, ok := r.take("spec"); ok {
		inner, isMap := asMap(v)
		if !isMap {
			return fmt.Errorf("taskSpec.spec must be a mapping")
		}
		return b.body(inner, nil)
	}
	return b.body(sm, r)
}

// reference reads a taskRef. A name with no resolver that a Task document of the files carries is read as the
// body. Any other reference is one StepTask step with the ref in Ext.
func (b *jobBuilder) reference(rm *omap, line int) error {
	name, _ := text(rm.vals["name"])
	_, resolver := rm.vals["resolver"]
	kind, _ := text(rm.vals["kind"])
	_, bundle := rm.vals["bundle"]
	if !resolver && !bundle && name != "" && (kind == "" || kind == kindTaskName) {
		if doc, ok := b.c.p.tasks[name]; ok {
			b.c.p.taskUsed[name] = true
			return b.document(doc, name)
		}
		b.remote(rm, line, "taskRef "+name+" names no Task in the parsed files")
		return nil
	}
	b.remote(rm, line, "remote taskRef ("+remoteKind(rm)+")")
	return nil
}

func remoteKind(rm *omap) string {
	if r, ok := text(rm.vals["resolver"]); ok {
		return r + " resolver"
	}
	if _, ok := rm.vals["bundle"]; ok {
		return "bundle"
	}
	return "unresolved name"
}

func (b *jobBuilder) remote(rm *omap, line int, why string) {
	id := fmt.Sprintf("%s-step-1", b.job.ID)
	sp := b.path + "/step/" + id
	k := newSink(b.c.p.rep, b.c.file, sp)
	k.keep("taskRef", rm, caps.StepAction, why+"; the reference is kept in Ext", line)
	b.job.Steps = append(b.job.Steps, &ir.Step{ID: id, Kind: ir.StepTask, Ext: k.ext()})
	if b.rawParams != nil {
		b.k.keep("params", b.rawParams, caps.ExtPassthrough, "the params of a remote task have no IR field; kept in Ext", line)
	}
}

// document reads the Task document that a taskRef names.
func (b *jobBuilder) document(doc *rawDoc, name string) error {
	meta, _ := asMap(doc.top.vals["metadata"])
	if meta != nil {
		rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
		for _, k := range meta.keys {
			if k != "name" {
				rest.set(k, meta.vals[k], meta.lines[k])
			}
		}
		if len(rest.keys) > 0 {
			b.k.keep("task_metadata", rest, caps.ExtPassthrough, "Task metadata has no IR field; kept in Ext", meta.lines["labels"])
		}
	}
	if name != b.c.plID+"-"+b.job.ID {
		b.k.vals["task_name"] = name
	}
	spec, ok := asMap(doc.top.vals["spec"])
	if !ok {
		return fmt.Errorf("task %s needs a spec", name)
	}
	return b.body(spec, nil)
}
