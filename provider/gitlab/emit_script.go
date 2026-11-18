package gitlab

import (
	"fmt"
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

	shellBash = "bash"
	shellSh   = "sh"
	noopLine  = "true"
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
	uploads  []string
	downs    []download
	art      artAcc
}

type download struct{ name string }

func newJobPrep(pid string, j *ir.Job) *jobPrep {
	return &jobPrep{j: j, path: jobPath(pid, j.ID), sections: map[string][]*yaml.Node{}}
}

func jobPath(pid, jid string) string { return "pipeline/" + pid + "/job/" + jid }

func stepPath(jp, sid string) string { return jp + "/step/" + sid }

// add appends a script item; comment lines queued earlier ride on it.
func (p *jobPrep) add(sec string, item *yaml.Node, head ...string) {
	lines := append(append([]string{}, p.pending...), head...)
	p.pending = nil
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
	for _, st := range p.j.Steps {
		if st == nil {
			continue
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
	}
	return nil
}

// stepGuard decides the shell condition around a step and the section it runs in. guard is empty when
// the step runs unconditionally; arg is the canonical text of an emulated condition.
func (e *emitter) stepGuard(sp string, st *ir.Step, sec string) (guard, arg, section string) {
	section = sec
	if st.If == nil {
		return "", "", sec
	}
	rest, status, ok := splitStatus(st.If)
	if !ok || status == "failure" {
		e.disable(sp, caps.StepIf, st.If, "step")
		return "false", "", sec
	}
	if status == "always" && sec != secAfter {
		section = secAfter
		e.emulate(sp, caps.StepIf, "moved to after_script", "after_script runs in a separate shell and its failure does not fail the job")
	}
	if rest == nil {
		return "", "", section
	}
	c, ok := e.condition(sp, caps.StepIf, rest, "step")
	if !ok {
		return "false", "", sec
	}
	g, ok := shellCond(c)
	if !ok {
		e.disable(sp, caps.StepIf, st.If, "step")
		return "false", "", sec
	}
	return g, expr.String(c), section
}

// scriptStep emits a script step: plain lines, or one block item when an emulation wraps them.
func (e *emitter) scriptStep(p *jobPrep, st *ir.Step) error {
	sp := stepPath(p.path, st.ID)
	if st.Image != nil {
		e.lose(sp, caps.StepImage, "GitLab sets the image per job, not per step")
	}
	if st.Shell != "" && st.Shell != shellBash && st.Shell != shellSh {
		e.lose(sp, caps.StepShell, "GitLab runs scripts in the runner's configured shell, not "+st.Shell)
	}
	if st.Timeout != nil {
		e.lose(sp, caps.StepTimeout, "GitLab has no per-step timeout; the job timeout applies")
	}
	guard, arg, sec := e.stepGuard(sp, st, sectionOf(st))
	lines, err := e.stepLines(sp, st)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		p.pending = append(p.pending, e.drain()...)
		return nil
	}
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
	if guard != "" && arg != "" {
		marks = append(marks, emitkit.Marker(caps.StepIf, sp), argPrefix+arg)
	}
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
	head := append(marks, e.drain()...)
	sub := dir != "" || len(exports) > 0
	if !sub && guard == "" {
		before := beforeLines(st, sec, len(lines))
		for i, l := range lines {
			target := sec
			if i < before {
				target = secBefore
			}
			if i == 0 {
				p.add(target, block(l), head...)
			} else {
				p.add(target, block(l))
			}
		}
		return nil
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
	p.add(sec, block(strings.Join(body, "\n")), head...)
	return nil
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
	var out []string
	for _, l := range st.Script {
		t, keep, err := e.scriptLine(sp, l)
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, t)
		}
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
		t, ok, err := e.value(sp, v.Value)
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
