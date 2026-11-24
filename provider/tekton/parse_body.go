package tekton

import (
	"fmt"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
)

// kindTaskName is the only taskRef kind that names a Task of the parsed files.
const kindTaskName = "Task"

// taskBody reads the Task that the PipelineTask runs: an inline taskSpec, a Task of the parsed files, or a
// remote Task that stays a reference.
func (b *jobBuilder) taskBody(r *reader) error {
	ref, hasRef := r.Take("taskRef")
	spec, hasSpec := r.Take("taskSpec")
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
		return b.reference(rm, r.Line("taskRef"))
	}
	if r.M.Vals["pipelineRef"] != nil || r.M.Vals["pipelineSpec"] != nil {
		b.k.Note(caps.ExtPassthrough, "a task that runs a pipeline has no IR form; the keys stay in Ext", r.Line("pipelineRef")+r.Line("pipelineSpec"))
		return nil
	}
	return fmt.Errorf("needs taskRef or taskSpec")
}

// inline reads a taskSpec: the Task fields sit beside an optional metadata key.
func (b *jobBuilder) inline(sm *omap) error {
	r := newReader(sm)
	if v, ok := r.Take("metadata"); ok {
		b.k.Keep("task_metadata", v, caps.ExtPassthrough, "taskSpec metadata has no IR field; kept in Ext", r.Line("metadata"))
	}
	if v, ok := r.Take("spec"); ok {
		inner, isMap := asMap(v)
		if !isMap {
			return fmt.Errorf("taskSpec.spec must be a mapping")
		}
		return b.body(inner, nil)
	}
	return b.body(sm, r)
}

// reference reads a taskRef. A name with no resolver that a Task document of the files carries is read as the
// body. A reference that names a catalog task is read as that task's mapping. Any other reference is one
// StepTask step with the ref in Ext.
func (b *jobBuilder) reference(rm *omap, line int) error {
	name, _ := text(rm.Vals["name"])
	_, resolver := rm.Vals["resolver"]
	kind, _ := text(rm.Vals["kind"])
	_, bundle := rm.Vals["bundle"]
	why := "remote taskRef (" + remoteKind(rm) + ")"
	if !resolver && !bundle && name != "" && (kind == "" || kind == kindTaskName) {
		if doc, ok := b.c.p.tasks[name]; ok {
			b.c.p.taskUsed[name] = true
			return b.document(doc, name)
		}
		why = "taskRef " + name + " names no Task in the parsed files"
	}
	entry, ok, err := catalogEntry(rm)
	if err != nil {
		return err
	}
	if ok {
		return b.catalog(rm, line, entry)
	}
	b.remote(rm, line, why)
	return nil
}

func remoteKind(rm *omap) string {
	if r, ok := text(rm.Vals["resolver"]); ok {
		return r + " resolver"
	}
	if _, ok := rm.Vals["bundle"]; ok {
		return "bundle"
	}
	return "unresolved name"
}

func (b *jobBuilder) remote(rm *omap, line int, why string) {
	id := fmt.Sprintf("%s-step-1", b.job.ID)
	sp := b.path + "/step/" + id
	k := newSink(b.c.p.rep, b.c.file, sp)
	k.Keep("taskRef", rm, caps.StepAction, why+"; the reference is kept in Ext", line)
	b.job.Steps = append(b.job.Steps, &ir.Step{ID: id, Kind: ir.StepTask, Ext: k.Ext()})
	if b.rawParams != nil {
		b.k.Keep("params", b.rawParams, caps.ExtPassthrough, "the params of a remote task have no IR field; kept in Ext", line)
	}
}

// document reads the Task document that a taskRef names.
func (b *jobBuilder) document(doc *rawDoc, name string) error {
	b.marks = readMarks(doc.Head)
	meta, _ := asMap(doc.Top.Vals["metadata"])
	if meta != nil {
		rest := ynode.NewMap()
		for _, k := range meta.Order {
			if k != "name" {
				rest.Set(k, meta.Vals[k], meta.Lines[k])
			}
		}
		if len(rest.Order) > 0 {
			b.k.Keep("task_metadata", rest, caps.ExtPassthrough, "Task metadata has no IR field; kept in Ext", meta.Lines["labels"])
		}
	}
	if name != b.c.plID+"-"+b.job.ID {
		b.k.Out["task_name"] = name
	}
	spec, ok := asMap(doc.Top.Vals["spec"])
	if !ok {
		return fmt.Errorf("task %s needs a spec", name)
	}
	return b.body(spec, nil)
}
