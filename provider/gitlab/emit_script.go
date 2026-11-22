package gitlab

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	secBefore = "before_script"
	secScript = "script"
	secAfter  = "after_script"

	noopLine = "true"
)

var sectionOrder = []string{secBefore, secScript, secAfter}

// jobPrep collects what a job's steps contribute: script items per section and the job keywords actions map to.
type jobPrep struct {
	j        *ir.Job
	sections map[string][]*yaml.Node
	path     string
	image    string
	vars     []ir.Var
	caches   []*omap
	pending  []string
	marks    []string
	uploads  []string
	downs    []download
	art      artAcc
	toAfter  bool
}

type download struct {
	step *ir.Step
	name string
	sp   string
}

func newJobPrep(path string, j *ir.Job) *jobPrep {
	return &jobPrep{j: j, path: path, sections: map[string][]*yaml.Node{}}
}

// lineCount is how many script lines a section holds so far, as Parse will count them.
func (p *jobPrep) lineCount(sec string) int {
	n := 0
	for _, it := range p.sections[sec] {
		n += lineCount(it.Value)
	}
	return n
}

func jobPath(pid, jid string) string { return "pipeline/" + pid + "/job/" + jid }

func stepPath(jp, sid string) string { return jp + "/step/" + sid }

// add appends a script item; comment lines queued earlier ride on it.
func (p *jobPrep) add(sec string, item *yaml.Node, head ...string) {
	lines := append(append([]string{}, p.pending...), head...)
	p.pending = nil
	if sec == secAfter {
		p.toAfter = true
	} else {
		lines = append(p.marks, lines...)
		p.marks = nil
	}
	if len(lines) > 0 {
		item.HeadComment = strings.Join(lines, "\n")
	}
	p.sections[sec] = append(p.sections[sec], item)
}

func sectionOf(st *ir.Step) string {
	if s, ok := extOf(st.Ext)[ExtSection].(string); ok {
		for _, known := range sectionOrder {
			if s == known {
				return s
			}
		}
	}
	return secScript
}

// prepSteps turns every step into script items or job keywords.
func (e *emitter) prepSteps(p *jobPrep) error {
	e.foreignExt(p.path, p.j.Ext)
	e.loseName(p.path, p.j.Name, p.j.ID)
	markSteps := needsStepMarks(p.j)
	for _, st := range p.j.Steps {
		if st == nil {
			continue
		}
		e.foreignExt(stepPath(p.path, st.ID), st.Ext)
		e.loseName(stepPath(p.path, st.ID), st.Name, "")
		mark := stepPrefix + commentSafe(st.ID)
		if markSteps {
			p.marks = append(p.marks, mark)
		}
		var err error
		if st.Kind == ir.StepScript {
			err = e.scriptStep(p, st)
		} else {
			err = e.actionStep(p, st)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", stepPath(p.path, st.ID), err)
		}
		// A step that moved to after_script is rebuilt from its own marker there, not from a step marker.
		if p.toAfter && len(p.marks) > 0 && p.marks[len(p.marks)-1] == mark {
			p.marks = p.marks[:len(p.marks)-1]
		}
		p.toAfter = false
	}
	return nil
}

// stepGuard decides the shell condition around a step and the section it runs in. guard is empty when
// the step runs unconditionally; arg is the canonical text of an emulated condition. moved is true when an
// always() step leaves the job's script for after_script.
func (e *emitter) stepGuard(sp string, st *ir.Step, sec string) (guard, arg, src, section string, moved bool) {
	section = sec
	if st.If == nil {
		return "", "", "", sec, false
	}
	rest, status, ok := splitStatus(st.If)
	if !ok || status == "failure" {
		e.disable(sp, caps.StepIf, st.If, "step")
		return disabledGuard, "", "", sec, false
	}
	if status == "always" && sec != secAfter {
		section, moved = secAfter, true
		e.emulate(sp, caps.StepIf, "moved to after_script", "after_script runs in a separate shell, so the step runs after the job's other lines and its failure does not fail the job")
	}
	if status == "success" {
		e.led.Note(e.use(sp, caps.StepIf), caps.Support{Level: caps.Native, Strategy: "success() is the default"}, "")
	}
	if rest == nil {
		return "", "", "", section, moved
	}
	c, ok := e.condition(sp, caps.StepIf, rest, "step")
	if !ok {
		return disabledGuard, "", "", sec, false
	}
	g, ok := shellCond(c)
	if !ok {
		e.disable(sp, caps.StepIf, st.If, "step")
		return disabledGuard, "", "", sec, false
	}
	return g, expr.String(c), expr.String(rest), section, moved
}

// stepLosses reports the step fields a GitLab script cannot carry at all.
func (e *emitter) stepLosses(sp string, st *ir.Step) {
	if st.Image != nil {
		e.lose(sp, caps.StepImage, "GitLab sets the image per job, not per step")
	}
	if st.Shell != "" {
		e.lose(sp, caps.StepShell, "GitLab runs scripts in the runner's configured shell, not "+st.Shell)
	}
	if st.Timeout != nil {
		e.lose(sp, caps.StepTimeout, "GitLab has no per-step timeout; the job timeout applies")
	}
}

// scriptStep emits a script step: plain lines, or one block item when an emulation wraps them.
func (e *emitter) scriptStep(p *jobPrep, st *ir.Step) error {
	sp := stepPath(p.path, st.ID)
	e.stepLosses(sp, st)
	lines, err := e.stepLines(sp, st)
	if err != nil {
		return err
	}
	return e.placeLines(p, st, lines)
}

// movedArg is the marker argument of an always() step that moved to after_script: where its lines start in
// that section, how many there are, and the continue-on-error flag the section forces on.
type movedArg struct {
	Pos    *int   `json:"pos,omitempty"`
	Moved  string `json:"moved"`
	Offset int    `json:"offset"`
	Lines  int    `json:"lines"`
	COE    bool   `json:"coe"`
}

// srcArg carries the condition as the source dialect wrote it, which the emitted guard only approximates.
type srcArg struct {
	Src  string `json:"src"`
	From string `json:"from,omitempty"`
}

// disabledGuard is the shell condition of a step whose own condition GitLab cannot state.
const disabledGuard = "false"

func lineCount(text string) int { return strings.Count(strings.TrimRight(text, "\n"), "\n") + 1 }

// placeLines writes a step's lines into its section, wrapped by the guards its if, workdir, env and
// continue-on-error fields call for. The collapsers in collapse.go invert each wrapper from its marker.
func (e *emitter) placeLines(p *jobPrep, st *ir.Step, lines []string) error {
	sp := stepPath(p.path, st.ID)
	if len(lines) == 0 {
		if st.If != nil {
			e.lose(sp, caps.StepIf, "the step has no line left to guard")
		}
		p.pending = append(p.pending, e.drain()...)
		return nil
	}
	guard, arg, src, sec, moved := e.stepGuard(sp, st, sectionOf(st))
	exports, err := e.exports(sp, st)
	if err != nil {
		return err
	}
	dir := ""
	if st.WorkDir != "" {
		if dir, err = e.field(sp, caps.ScriptInterpolation, st.WorkDir); err != nil {
			return err
		}
	}
	var marks []string
	if guard == disabledGuard && arg == "" {
		arg = disabledGuard
	}
	if (guard != "" && arg != "") || moved {
		marks = append(marks, emitkit.Marker(caps.StepIf, sp))
	}
	if guard != "" && arg != "" {
		a, err := markerArg(arg)
		if err != nil {
			return err
		}
		marks = append(marks, a)
		if e.from != nil && src != "" && src != arg {
			a, err := markerArg(srcArg{Src: src})
			if err != nil {
				return err
			}
			marks = append(marks, a)
		}
	}
	ifEnd := len(marks)
	if dir != "" {
		marks = append(marks, emitkit.Marker(caps.StepWorkdir, sp))
	}
	if len(exports) > 0 {
		marks = append(marks, emitkit.Marker(caps.StepEnv, sp))
	}
	// after_script ignores its own exit status, so GitLab carries continue-on-error natively there.
	if st.ContinueOnError && sec != secAfter {
		marks = append(marks, emitkit.Marker(caps.StepContinueOnError, sp))
		for i, l := range lines {
			lines[i] = orTrue(l)
		}
	}
	items, targets := wrapLines(st, sec, lines, guard, dir, exports)
	if moved {
		pos := slices.Index(p.j.Steps, st)
		mv := movedArg{Moved: "always", Offset: p.lineCount(secAfter), COE: st.ContinueOnError, Pos: &pos}
		for _, it := range items {
			mv.Lines += lineCount(it)
		}
		a, err := markerArg(mv)
		if err != nil {
			return err
		}
		// The argument must follow its own marker; after another marker it would not read as the argument of this one.
		marks = slices.Insert(marks, ifEnd, a)
	}
	head := append(marks, e.drain()...)
	for i, it := range items {
		if i == 0 {
			p.add(targets[i], block(it), head...)
		} else {
			p.add(targets[i], block(it))
		}
	}
	return nil
}

// wrapLines returns the script items for a step and the section each goes in. Plain lines split between
// before_script and script; any wrapper turns the step into one item so it runs in one shell.
func wrapLines(st *ir.Step, sec string, lines []string, guard, dir string, exports []string) (items, targets []string) {
	sub := dir != "" || len(exports) > 0
	if !sub && guard == "" {
		before := beforeLines(st, sec, len(lines))
		for i, l := range lines {
			t := sec
			if i < before {
				t = secBefore
			}
			items, targets = append(items, l), append(targets, t)
		}
		return items, targets
	}
	body := lines
	if sub {
		inner := []string{"("}
		if dir != "" {
			inner = append(inner, "cd "+dq(dir)+" || exit 1")
		}
		inner = append(inner, exports...)
		body = append(append(inner, lines...), ")")
	}
	if guard != "" {
		body = append(append([]string{"if " + guard + "; then"}, body...), "fi")
	}
	return []string{strings.Join(body, "\n")}, []string{sec}
}

// beforeLines is how many leading lines return to before_script; only plain script-section lines split, because a
// wrapped step is one block that must stay in one shell.
func beforeLines(st *ir.Step, sec string, n int) int {
	v, _ := extOf(st.Ext)[ExtBeforeLines].(int)
	if f, ok := extOf(st.Ext)[ExtBeforeLines].(float64); ok {
		v = int(f)
	}
	if sec != secScript || v < 0 {
		return 0
	}
	return min(v, n)
}

// orTrue appends the continue-on-error guard; a line with a comment or a newline goes inside braces so the guard is not swallowed.
func orTrue(l string) string {
	if strings.ContainsAny(l, "#\n") {
		return "{\n" + l + "\n} || true"
	}
	return l + continueSufx
}

func (e *emitter) stepLines(sp string, st *ir.Step) ([]string, error) {
	texts := make([]string, len(st.Script))
	outs := make([]string, len(st.Script))
	why := make([]string, len(st.Script))
	for i, l := range st.Script {
		texts[i] = emitkit.NeutralizeResultWrite(e.lose, sp, l)
		var err error
		if outs[i], why[i], err = e.scriptLine(texts[i]); err != nil {
			return nil, err
		}
	}
	keep := emitkit.DropLost(texts, why, func(detail, text string) {
		e.lose(sp, caps.ScriptInterpolation, detail)
		e.comment("script.interpolation " + sp + ": " + text)
	})
	out := make([]string, 0, len(keep))
	for _, i := range keep {
		out = append(out, outs[i])
	}
	return out, nil
}

// exports renders step env as export lines; a value GitLab cannot carry on one line is lost.
func (e *emitter) exports(sp string, st *ir.Step) ([]string, error) {
	var out []string
	for _, v := range st.Env {
		if !isIdent(v.Name) {
			e.lose(sp, caps.StepEnv, "variable name "+v.Name+" is not a shell identifier")
			continue
		}
		t, ok, err := e.value(sp, caps.StepEnv, v.Value)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if strings.Contains(t, "\n") {
			e.lose(sp, caps.StepEnv, "variable "+v.Name+" holds a newline")
			continue
		}
		out = append(out, "export "+v.Name+"="+dq(t))
	}
	return out, nil
}

// needsStepMarks is false when Parse's one-step reading of the script already gives the job's steps back.
func needsStepMarks(j *ir.Job) bool {
	scripts := 0
	for _, st := range j.Steps {
		if st == nil {
			continue
		}
		if sectionOf(st) == secScript {
			scripts++
			if st.ID != "script" {
				return true
			}
		}
	}
	return scripts > 1
}
