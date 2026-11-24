package woodpecker

import (
	"strings"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
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
// Every action step leaves a marker that holds it, so the parser can put it back where it stood.
func (e *emitter) actionStep(c *wfCtx, sp string, st *ir.Step, idx int) (*wmap, error) {
	image, lost, err := e.mapAction(c, sp, st)
	if err != nil {
		return nil, err
	}
	if lost != "" {
		return e.failingStep(c, sp, st, idx, caps.StepAction, lost)
	}
	return nil, e.markAction(sp, st, idx, image, false)
}

// failingStep writes the step that stands for a step with no Woodpecker form: it prints the loss and exits 1,
// and the marker holds the source step.
func (e *emitter) failingStep(c *wfCtx, sp string, st *ir.Step, idx int, f caps.Feature, why string) (*wmap, error) {
	fail := &ir.Step{ID: st.ID, Kind: ir.StepScript, Image: st.Image, Env: st.Env, Script: []string{emitkit.FailLoud(f, sp, why)}}
	m, err := e.scriptStep(c, sp, fail)
	if err != nil {
		return nil, err
	}
	return m, e.markAction(sp, st, idx, "", true)
}

// mapAction applies the action to the workflow and returns the image it selected, if any.
// lost is the reason when the action has no Woodpecker form and a failing step must stand in for it.
func (e *emitter) mapAction(c *wfCtx, sp string, st *ir.Step) (image, lost string, err error) {
	if st.Action == nil || st.Kind == ir.StepPipelineCall {
		why := "step kind " + string(st.Kind) + " has no Woodpecker form"
		e.dropAction(sp, st, why)
		return "", why, nil
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
	ok = ok && !e.gated[st]
	if !ok {
		why := "no Woodpecker mapping for action " + actionRef(st.Action)
		e.dropAction(sp, st, why)
		return "", why, nil
	}
	e.led.Note(use(sp, caps.StepAction), t.Support, t.Support.Caveat)
	for _, k := range t.Unmapped {
		e.lose(sp, caps.StepAction, "input "+k+" has no Woodpecker equivalent")
	}
	if t.Support.Level == caps.Lost {
		e.comment("step " + sp + " uses " + actionRef(st.Action) + ": " + t.Support.Caveat)
		return "", "the action has no Woodpecker form: " + t.Support.Caveat, nil
	}
	if detail := "the action reference " + actionRef(st.Action) + " is not in the Woodpecker file; it is kept in round-trip metadata"; t.WritesEquivalent() {
		e.led.Retain(use(sp, caps.ExtPassthrough), detail)
	} else {
		e.lose(sp, caps.ExtPassthrough, detail)
	}
	e.unapplied(sp, st)
	return e.applyTarget(c, sp, t)
}

// actionArg carries the step an action became, so the parser can put it back where it stood.
type actionArg struct {
	Step  string `json:"step"`
	Image string `json:"image,omitempty"`
	At    int    `json:"at"`
	// Fail is true when a failing step stands in the file at At for the held step.
	Fail bool `json:"fail,omitempty"`
}

// markAction writes the marker that holds a step the file does not have. The step keeps no condition: the IR
// states conditions in the dialect of its origin, and a restored one would read in the wrong dialect.
func (e *emitter) markAction(sp string, st *ir.Step, idx int, image string, fail bool) error {
	held := *st
	held.If = nil
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		return err
	}
	return e.markArg(caps.StepAction, sp, actionArg{Step: body, At: idx, Image: image, Fail: fail})
}

// withoutBookkeeping drops the keys a parser sets only to rebuild its own layout, and the Ext of other providers; they mean nothing to the
// provider that reads the step back.
func withoutBookkeeping(e ir.Ext) ir.Ext {
	var out ir.Ext
	for prov, raw := range e {
		if prov != dialect {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			if out == nil {
				out = ir.Ext{}
			}
			out[prov] = raw
			continue
		}
		kept := map[string]any{}
		for k, v := range m {
			if !emitkit.BookkeepingExt[k] {
				kept[k] = v
			}
		}
		if len(kept) > 0 {
			if out == nil {
				out = ir.Ext{}
			}
			out[prov] = kept
		}
	}
	return out
}

// dropAction reports a step that cannot be written and keeps its action reference in a comment.
func (e *emitter) dropAction(sp string, st *ir.Step, why string) {
	e.lose(sp, caps.StepAction, why)
	ref := string(st.Kind)
	if st.Action != nil {
		ref = actionRef(st.Action)
	}
	e.comment("step " + sp + " uses " + ref)
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

// applyTarget applies a mapped action to the workflow and returns the image it selected, if any.
func (e *emitter) applyTarget(c *wfCtx, sp string, t actions.Target) (image, lost string, err error) {
	switch t.Kind {
	case actions.KindImplicit:
		if d, ok := t.With["depth"].(string); ok {
			c.depth = d
		}
	case actions.KindImage:
		img, err := e.text(nil, sp, caps.StepAction, valueText, t.Image)
		if err != nil {
			return "", "", err
		}
		c.image = img
		return img, "", nil
	case actions.KindCache:
	case actions.KindUpload:
		name, _ := t.With["name"].(string)
		c.uploads[name] = true
	case actions.KindDownload:
		name, _ := t.With["name"].(string)
		if !c.uploads[name] && (name != "" || len(c.uploads) == 0) {
			e.lose(sp, caps.StepAction, "no upload of "+strings.TrimSpace("artifact "+name)+" in this workflow; workflows do not share a workspace")
			e.comment("step " + sp + " downloads " + name)
			return "", "no upload of the artifact in this workflow", nil
		}
	default:
		why := "action kind " + t.Kind + " has no Woodpecker form"
		e.lose(sp, caps.StepAction, why)
		return "", why, nil
	}
	return "", "", nil
}
