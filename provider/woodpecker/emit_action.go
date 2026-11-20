package woodpecker

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// actionRef spells an action reference the way a workflow does, for messages.
func actionRef(a *ir.ActionRef) string {
	switch a.Kind {
	case ir.ActionDocker:
		return "docker://" + a.Image
	case ir.ActionLocal:
		return a.Path
	}
	s := a.Owner + "/" + a.Repo
	if a.Path != "" {
		s += "/" + a.Path
	}
	if a.Ref != "" {
		s += "@" + a.Ref
	}
	return s
}

// actionStep maps an action step through the actions table. Woodpecker has no step for any mapped kind: the
// action becomes a clone setting, an image choice or the shared workspace, so the step itself is not written.
func (e *emitter) actionStep(c *wfCtx, sp string, st *ir.Step) (*wmap, error) {
	if st.Action == nil || st.Kind == ir.StepPipelineCall {
		e.dropAction(sp, st, "step kind "+string(st.Kind)+" has no Woodpecker form")
		return nil, nil
	}
	with := map[string]string{}
	for k, v := range st.With {
		s, ok := valueCanon(v)
		if !ok {
			e.lose(sp, caps.StepAction, "input "+k+" is an expression with no workflow spelling")
			continue
		}
		with[k] = s
	}
	t, ok := actions.Lookup(*st.Action, dialect, with)
	if !ok {
		e.dropAction(sp, st, "no Woodpecker mapping for action "+actionRef(st.Action))
		return nil, nil
	}
	e.led.Note(use(sp, caps.StepAction), t.Support, t.Support.Caveat)
	for _, k := range t.Unmapped {
		e.lose(sp, caps.StepAction, "input "+k+" has no Woodpecker equivalent")
	}
	if t.Support.Level == caps.Lost {
		e.comment("step " + sp + " uses " + actionRef(st.Action) + ": " + t.Support.Caveat)
		e.unapplied(sp, st)
		return nil, nil
	}
	e.lose(sp, caps.ExtPassthrough, "the action reference "+actionRef(st.Action)+" is not in the Woodpecker file; it is kept in round-trip metadata")
	e.unapplied(sp, st)
	return nil, e.applyTarget(c, sp, t)
}

// dropAction reports a step that cannot be written and keeps its action reference in a comment.
func (e *emitter) dropAction(sp string, st *ir.Step, why string) {
	e.lose(sp, caps.StepAction, why)
	ref := string(st.Kind)
	if st.Action != nil {
		ref = actionRef(st.Action)
	}
	e.comment("step " + sp + " uses " + ref)
	e.unapplied(sp, st)
}

// unapplied reports the step fields that have nothing to attach to once the action is not a step.
func (e *emitter) unapplied(sp string, st *ir.Step) {
	why := "the action is not written as a step"
	if st.If != nil {
		e.lose(sp, caps.StepIf, "a step condition cannot guard an action that is not a step: "+why)
	}
	if len(st.Env) > 0 {
		e.lose(sp, caps.StepEnv, "step env has no step to attach to: "+why)
	}
	if st.WorkDir != "" {
		e.lose(sp, caps.StepWorkdir, "step working directory has no step to attach to: "+why)
	}
	if st.ContinueOnError {
		e.lose(sp, caps.StepContinueOnError, "continue-on-error has no step to attach to: "+why)
	}
	e.stepLosses(sp, st)
}

func (e *emitter) applyTarget(c *wfCtx, sp string, t actions.Target) error {
	switch t.Kind {
	case actions.KindImplicit:
		if d, ok := t.With["depth"].(string); ok {
			c.depth = d
		}
	case actions.KindImage:
		img, err := e.text(nil, sp, caps.StepAction, valueText, t.Image)
		if err != nil {
			return err
		}
		c.image = img
		e.mark(caps.StepAction, sp)
	case actions.KindCache:
		e.mark(caps.StepAction, sp)
	case actions.KindUpload:
		name, _ := t.With["name"].(string)
		c.uploads[name] = true
		e.mark(caps.StepAction, sp)
	case actions.KindDownload:
		name, _ := t.With["name"].(string)
		if !c.uploads[name] && (name != "" || len(c.uploads) == 0) {
			e.lose(sp, caps.StepAction, "no upload of "+strings.TrimSpace("artifact "+name)+" in this workflow; workflows do not share a workspace")
			e.comment("step " + sp + " downloads " + name)
			return nil
		}
		e.mark(caps.StepAction, sp)
	default:
		e.lose(sp, caps.StepAction, "action kind "+t.Kind+" has no Woodpecker form")
	}
	return nil
}
