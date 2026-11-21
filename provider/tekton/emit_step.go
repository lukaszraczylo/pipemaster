package tekton

import (
	"path"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	onErrorContinue = "continue"
	// workspaceDir is the directory Tekton shares between the steps of a Task and where the clone lands.
	workspaceDir = "/workspace"
)

// stepBook is what a job's step loop needs beyond one step.
type stepBook struct {
	guard expr.Expr
	names map[string]bool
	nodes []*yaml.Node
	seq   int
}

func (tc *taskCtx) durationText(d *ir.Duration) string { return time.Duration(*d).String() }

// stepName gives the step a DNS-1123 name unique in the Task and the ID comment when it differs.
func (tc *taskCtx) stepName(b *stepBook, sp string, st *ir.Step) (string, string) {
	name := tc.e.dnsName(b.names, sp, "step id", st.ID, maxName, true)
	if name != st.ID {
		return name, idComment(st.ID)
	}
	return name, ""
}

// stepOutputs holds a written step; node is nil when the step is not written.
func (tc *taskCtx) step(b *stepBook, st *ir.Step, sp string) (*wmap, error) {
	name, comment := tc.stepName(b, sp, st)
	var m *wmap
	var err error
	switch {
	case st.Kind == ir.StepAction || st.Action != nil || len(st.With) > 0:
		m, err = tc.actionStep(b, st, sp, name)
	case st.Kind == ir.StepScript || st.Kind == "":
		m, err = tc.scriptStep(b, st, sp, name)
	default:
		tc.e.lose(sp, caps.StepAction, "step kind "+string(st.Kind)+" has no Tekton form")
		tc.e.comment(sp, "step "+sp+" has kind "+string(st.Kind))
		err = tc.markAction(b, sp, st, "", nil)
	}
	if m != nil && comment != "" {
		m.n.HeadComment = comment
	}
	return m, err
}

// scriptStep writes a script step. An expression with no spelling comments out its line.
func (tc *taskCtx) scriptStep(b *stepBook, st *ir.Step, sp, name string) (*wmap, error) {
	sc := newScope()
	m := newMap()
	m.setStr("name", name)
	if st.Name != "" {
		m.setStr("displayName", st.Name)
	}
	userEnv, err := tc.envList(sc, sp, st.Env)
	if err != nil {
		return nil, err
	}
	for _, n := range userEnv {
		sc.names[envName(n)] = true
	}
	image, err := tc.stepImage(sc, st, sp)
	if err != nil {
		return nil, err
	}
	if image != "" {
		m.setStr("image", image)
	}
	cond := tc.cond(sp, caps.StepIf, st.If, taskScope)
	guard := andGuard(b.guard, cond.guard)
	lines, err := tc.scriptLines(sc, st, sp)
	if err != nil {
		return nil, err
	}
	if err := tc.markShell(sc, sp, lines); err != nil {
		return nil, err
	}
	if guard != nil {
		if lines, err = exprtekton.AddGuard(lines, guard); err != nil {
			return nil, err
		}
		guardEnv(sc, guard)
	}
	if st.Shell != "" {
		lines = append([]string{shebang + st.Shell}, lines...)
	}
	if len(lines) > 0 && !tc.commandForm(m, st, sp, guard) {
		m.set("script", block(strings.Join(lines, "\n")))
	}
	if len(userEnv)+len(sc.env) > 0 {
		m.set("env", seqOf(append(userEnv, sc.env...)...))
	}
	if st.WorkDir != "" {
		wd, ok, err := tc.text(sc, sp, caps.StepWorkdir, valueText, st.WorkDir)
		if err != nil {
			return nil, err
		}
		if ok {
			if tc.checkout && !path.IsAbs(wd) {
				if err := tc.e.markArg(caps.StepWorkdir, sp, workdirArg{WorkDir: st.WorkDir, Wrote: path.Join(workspaceDir, st.WorkDir)}); err != nil {
					return nil, err
				}
				wd = path.Join(workspaceDir, wd)
			}
			m.setStr("workingDir", wd)
		}
	}
	tc.stepTail(m, st, cond)
	return m, tc.stepExt(m, st, sp)
}

func envName(n *yaml.Node) string {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "name" {
			return n.Content[i+1].Value
		}
	}
	return ""
}

// stepTail writes the keys every step kind shares: timeout, onError and when.
func (tc *taskCtx) stepTail(m *wmap, st *ir.Step, cond condition) {
	if st.Timeout != nil {
		m.setStr("timeout", tc.durationText(st.Timeout))
	}
	if st.ContinueOnError {
		m.setStr("onError", onErrorContinue)
	}
	if len(cond.when) > 0 {
		m.set("when", whenNode(cond.when))
	}
}

// stepExt writes the Tekton Ext keys of a step that came from Tekton.
func (tc *taskCtx) stepExt(m *wmap, st *ir.Step, sp string) error {
	x := extOf(st.Ext)
	if len(x) == 0 || !tc.e.own {
		return nil
	}
	skip := map[string]bool{ExtCommand: true, ExtArgs: true, ExtScriptLine: true, "ref": true, "params": true, "taskRef": true}
	for _, k := range sortedKeys(x) {
		if k == "timeout" || k == "onError" {
			continue
		}
		if !skip[k] {
			tc.e.lose(sp, caps.ExtPassthrough, "key "+k+" of the step has no Tekton place in the emitter")
		}
	}
	for _, k := range []string{"timeout", "onError"} {
		if v, ok := x[k]; ok {
			n, err := plainNode(v)
			if err != nil {
				return err
			}
			m.set(k, n)
		}
	}
	return nil
}

// scriptLines converts the script of a step.
func (tc *taskCtx) scriptLines(sc *stepScope, st *ir.Step, sp string) ([]string, error) {
	var lines []string
	for _, l := range st.Script {
		out, err := tc.scriptLine(sc, sp, l)
		if err != nil {
			return nil, err
		}
		lines = append(lines, out)
	}
	return lines, nil
}

// stepImage picks the image of a step: its own, the one a setup action chose, or the default. The job
// container is the stepTemplate image, so a step that wants it writes none.
func (tc *taskCtx) stepImage(sc *stepScope, st *ir.Step, sp string) (string, error) {
	if st.Image != nil && st.Image.Ref != "" {
		if len(st.Image.Entrypoint) > 0 {
			tc.e.lose(sp, caps.StepImage, "the image entrypoint has no Tekton form beside a script")
		}
		img, ok, err := tc.text(sc, sp, caps.StepImage, valueText, st.Image.Ref)
		if err != nil {
			return "", err
		}
		if ok && (tc.jp.j.Container == nil || !tc.sameAsContainer(img)) {
			return img, nil
		}
		if ok {
			return "", nil
		}
	}
	if tc.image != "" {
		return tc.image, nil
	}
	if tc.jp.j.Container != nil && tc.containerRef != "" {
		return "", nil
	}
	tc.e.emulate(sp, caps.StepImage, "default image", "the step names no image and Tekton requires one; "+defaultImage+" runs it")
	if err := tc.e.markArg(caps.StepImage, sp, imageArg{Image: defaultImage}); err != nil {
		return "", err
	}
	return defaultImage, nil
}

func (tc *taskCtx) sameAsContainer(img string) bool {
	return tc.containerRef != "" && tc.containerRef == img
}

// andGuard joins two guard conditions.
func andGuard(a, b expr.Expr) expr.Expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return expr.Binary{Op: expr.OpAnd, L: a, R: b}
}

// commandForm writes command and args back for a step that came as command and args, while its line is as
// the parser wrote it. A step that changed becomes a script and reports that a shell must exist in the image.
func (tc *taskCtx) commandForm(m *wmap, st *ir.Step, sp string, guard expr.Expr) bool {
	x := extOf(st.Ext)
	if !tc.e.own || x[ExtCommand] == nil {
		return false
	}
	line, _ := x[ExtScriptLine].(string)
	if guard != nil || st.Shell != "" || len(st.Script) != 1 || st.Script[0] != line {
		tc.e.emulate(sp, caps.StepScript, "script step", "command and args are written as a script, so the image needs a shell")
		return false
	}
	for _, k := range []string{ExtCommand, ExtArgs} {
		if v, ok := x[k]; ok {
			if n := tc.e.node(sp, k, v); n != nil {
				m.set(k, n)
			}
		}
	}
	return true
}

// shellUse is one reference that a script reads as a shell variable.
type shellUse struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
	// At lists which occurrences of ${Name} in the script, counted from 0, the reference wrote.
	At []int `json:"at"`
	// Of is how many occurrences of ${Name} the script holds.
	Of int `json:"of"`
}

// shellArg lists the references of a step script that became shell variables.
type shellArg struct {
	Refs []shellUse `json:"refs"`
}

// markShell writes the shell variable of each reference in place of its placeholder, and the marker that says
// where they stand. It changes lines in place.
func (tc *taskCtx) markShell(sc *stepScope, sp string, lines []string) error {
	if len(sc.shell) == 0 {
		return nil
	}
	names := map[string]bool{}
	for _, n := range sc.shell {
		names[n] = true
	}
	var alt []string
	for n := range names {
		alt = append(alt, regexp.QuoteMeta("${"+n+"}"))
	}
	re := regexp.MustCompile(strings.Join(alt, "|") + "|" + shellMark + "[^" + shellMark + "]+" + shellMark)
	seen := map[string]int{}
	uses := map[string]*shellUse{}
	for i, l := range lines {
		lines[i] = re.ReplaceAllStringFunc(l, func(m string) string {
			ref, isRef := strings.CutPrefix(m, shellMark)
			if !isRef {
				seen[m[2:len(m)-1]]++
				return m
			}
			ref = strings.TrimSuffix(ref, shellMark)
			name := sc.shell[ref]
			u := uses[ref]
			if u == nil {
				u = &shellUse{Ref: ref, Name: name}
				uses[ref] = u
			}
			u.At = append(u.At, seen[name])
			seen[name]++
			return "${" + name + "}"
		})
	}
	var arg shellArg
	for _, ref := range sortedKeys(uses) {
		u := uses[ref]
		u.Of = seen[u.Name]
		arg.Refs = append(arg.Refs, *u)
	}
	if len(arg.Refs) == 0 {
		return nil
	}
	for _, u := range arg.Refs {
		if !strings.HasPrefix(u.Ref, expr.PrefixEnv+".") {
			tc.e.emulate(sp, caps.ScriptInterpolation, "shell variable", "a secret or variable in the script is read as a shell variable from the step env")
			break
		}
	}
	return tc.e.markArg(caps.ScriptInterpolation, sp, arg)
}
