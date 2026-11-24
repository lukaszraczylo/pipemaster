package drone

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// carryYAML writes v in the IR's YAML form, which keeps conditions and values that JSON cannot hold.
func carryYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

func triggerPath(pid string, i int) string { return "pipeline/" + pid + "/trigger/" + strconv.Itoa(i) }

// posArg points a marker at a step by its place in the document.
type posArg struct {
	// At is set for a script step: where its converted references stand among the ${NAME} of the script.
	At   map[string]refAt `json:"at,omitempty"`
	Refs []string         `json:"refs,omitempty"`
	Pos  int              `json:"pos"`
}

// finallyArg carries what the finally document lost: its own needs, which the all-documents dependency
// replaces, and whether it had a condition, because the status gate reads back as one.
type finallyArg struct {
	Needs []ir.Need `json:"needs,omitempty"`
	NoIf  bool      `json:"noIf,omitempty"`
}

// multiArg says which pipeline and job a document came from when an emitter folded several pipelines into one file.
type multiArg struct {
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
	P        int    `json:"p"`
	J        int    `json:"j"`
}

// cellArg names the IR job and the matrix cell of a document. Matrix and Held are set on the first cell only.
type cellArg struct {
	Matrix *ir.Matrix `json:"matrix,omitempty"`
	// Services holds the image of each service whose image reads a matrix name, by service name.
	Services map[string]string `json:"services,omitempty"`
	Job      string            `json:"job"`
	// Env is a YAML list of the job variables whose value reads a matrix name.
	Env   string     `json:"env,omitempty"`
	Cell  ir.Combo   `json:"cell"`
	Held  []heldText `json:"held,omitempty"`
	Index int        `json:"index"`
}

// heldText is the text of a written step that reads a matrix name, as the IR had it before the cell values
// replaced the references. Pos is the place of the step in the document.
type heldText struct {
	Image   string `json:"image,omitempty"`
	WorkDir string `json:"workdir,omitempty"`
	// Env and With are YAML lists of the variables and settings whose value reads a matrix name.
	Env    string   `json:"env,omitempty"`
	With   string   `json:"with,omitempty"`
	Script []string `json:"script,omitempty"`
	Pos    int      `json:"pos"`
}

// containerArg is the job image, and the number of steps that took it when the file was written.
type containerArg struct {
	ir.Image
	Taken int `json:"taken,omitempty"`
}

// renameArg carries the job id of a document whose name is the id made valid.
type renameArg struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// globArg carries the glob filter of a trigger whose patterns the file spells another way, such as a ! entry
// that becomes an exclude.
type globArg struct {
	Branches ir.Filter `json:"branches"`
	Tags     ir.Filter `json:"tags"`
}

// actionArg carries the step an action became, so the parser can put it back where it stood.
type actionArg struct {
	Step  string `json:"step"`
	Image string `json:"image,omitempty"`
	Stand string `json:"stand,omitempty"`
	At    int    `json:"at"`
	Fail  bool   `json:"fail,omitempty"`
}

// markTriggers writes a marker whose argument holds the pipeline triggers as the IR had them, because a file
// holds them only as conditions that change a glob's meaning or replace the triggers of a manual document.
func (e *emitter) markTriggers(f caps.Feature, path string, ts []ir.Trigger) error {
	body, err := carryYAML(ts)
	if err != nil {
		return err
	}
	return e.markArg(f, path, body)
}

// markVars writes a marker whose argument holds the variables copied into every document or step.
func (e *emitter) markVars(f caps.Feature, path string, vs []ir.Var) error {
	body, err := carryYAML(vs)
	if err != nil {
		return err
	}
	return e.markArg(f, path, body)
}

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

// actionStep maps an action step through the actions table. Drone has no step for any mapped kind: the action
// becomes a clone setting, an image choice or the shared workspace, so the step itself is not written. Every
// action step leaves a marker that holds it, so the parser can put it back where it stood.
func (e *emitter) actionStep(c *docCtx, sp string, st *ir.Step) (*ymap, error) {
	image, lost, err := e.mapAction(c, sp, st)
	if err != nil {
		return nil, err
	}
	if lost != "" {
		return e.failingStep(c, sp, st, caps.StepAction, lost)
	}
	if c.emul != nil {
		return e.standStep(c, sp, st, c.pos)
	}
	return nil, e.markAction(sp, st, c.pos, image, false)
}

// failingStep writes the step that stands for a step with no Drone form: it prints the loss and exits 1, keeps
// the condition and the failure policy of the source step, and the marker holds the source step.
func (e *emitter) failingStep(c *docCtx, sp string, st *ir.Step, f caps.Feature, why string) (*ymap, error) {
	fail := &ir.Step{ID: st.ID, Kind: ir.StepScript, Image: st.Image, Env: st.Env, Script: []string{emitkit.FailLoud(f, sp, why)}}
	m, err := e.scriptStep(c, sp, fail)
	if err != nil {
		return nil, err
	}
	return m, e.markAction(sp, st, c.pos, "", true)
}

// mapAction applies the action to the document and returns the image it selected, if any. lost is the reason
// when the action has no Drone form and a failing step must stand in for it.
func (e *emitter) mapAction(c *docCtx, sp string, st *ir.Step) (image, lost string, err error) {
	if st.Action == nil || st.Kind == ir.StepPipelineCall {
		why := "step kind " + string(st.Kind) + " has no Drone form"
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
		why := "no Drone mapping for action " + actionRef(st.Action)
		e.dropAction(sp, st, why)
		return "", why, nil
	}
	e.led.Note(use(sp, caps.StepAction), t.Support, t.Support.Caveat)
	for _, k := range t.Unmapped {
		e.lose(sp, caps.StepAction, "input "+k+" has no Drone equivalent")
	}
	if t.Support.Level == caps.Lost {
		e.comment("step " + sp + " uses " + actionRef(st.Action) + ": " + t.Support.Caveat)
		return "", "the action has no Drone form: " + t.Support.Caveat, nil
	}
	if detail := "the action reference " + actionRef(st.Action) + " is not in the Drone file; it is kept in round-trip metadata"; t.WritesEquivalent() {
		e.led.Retain(use(sp, caps.ExtPassthrough), detail)
	} else {
		e.lose(sp, caps.ExtPassthrough, detail)
	}
	e.unapplied(sp, st)
	return e.applyTarget(c, sp, st, t)
}

// markAction writes the marker that holds a step the file does not have. The step keeps no condition: the IR
// states conditions in the dialect of its origin, and a restored one would read in the wrong dialect.
func (e *emitter) markAction(sp string, st *ir.Step, at int, image string, fail bool) error {
	held := *st
	held.If = nil
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		return err
	}
	return e.markArg(caps.StepAction, sp, actionArg{Step: body, At: at, Image: image, Fail: fail})
}

// withoutBookkeeping drops the keys a parser sets only to rebuild its own layout, and the Ext of other providers.
func withoutBookkeeping(x ir.Ext) ir.Ext {
	var out ir.Ext
	for prov, raw := range x {
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

// applyTarget applies a mapped action to the document and returns the image it selected, if any.
func (e *emitter) applyTarget(c *docCtx, sp string, st *ir.Step, t actions.Target) (image, lost string, err error) {
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
	case actions.KindScript:
		if d, ok := t.With["depth"].(string); ok {
			c.depth = d
		}
		c.emul = emitkit.ScriptStep(st, t, sp, c.image == "")
		e.explainUnspelled(sp, st, t)
	case actions.KindCache:
	case actions.KindUpload:
		name, _ := t.With["name"].(string)
		c.uploads[name] = true
	case actions.KindDownload:
		name, _ := t.With["name"].(string)
		if !c.uploads[name] && (name != "" || len(c.uploads) == 0) {
			e.lose(sp, caps.StepAction, "no upload of "+strings.TrimSpace("artifact "+name)+" in this document; documents do not share a workspace")
			e.comment("step " + sp + " downloads " + name)
			return "", "no upload of the artifact in this document", nil
		}
	default:
		why := "action kind " + t.Kind + " has no Drone form"
		e.lose(sp, caps.StepAction, why)
		return "", why, nil
	}
	return "", "", nil
}

// standStep writes the script step that stands for an action whose inputs a script carries. The marker holds
// the action step and the script, so the parser puts the action back when the file still has the script.
func (e *emitter) standStep(c *docCtx, sp string, st *ir.Step, idx int) (*ymap, error) {
	cp := c.emul
	c.emul = nil
	m, err := e.scriptStep(c, sp, cp)
	if err != nil {
		return nil, err
	}
	return m, e.markStand(sp, st, idx, strings.Join(cp.Script, "\n"))
}

// markStand is markAction for a step that a script stands for.
func (e *emitter) markStand(sp string, st *ir.Step, at int, script string) error {
	held := *st
	held.If = nil
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		return err
	}
	return e.markArg(caps.StepAction, sp, actionArg{Step: body, At: at, Stand: script})
}

// explainUnspelled reports why an emulated step stops: an input whose value has no Drone spelling is not set,
// and the guard of the step then fails before git runs.
func (e *emitter) explainUnspelled(sp string, st *ir.Step, t actions.Target) {
	for _, in := range slices.Sorted(maps.Values(t.Env)) {
		v := st.With[in]
		if v.Kind != ir.ValueExpr {
			continue
		}
		if _, lost := e.convertParts(nil, valueText, emitkit.ValueParts(v)); len(lost) > 0 {
			e.lose(sp, caps.StepAction, "input "+in+" has no value on this target: its value "+strings.Join(lost, ", ")+" is a pipeline parameter or an expression with no Drone spelling, so the step stops before it runs git")
		}
	}
}
