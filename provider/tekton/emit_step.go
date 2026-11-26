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
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
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
	m, ok, err := tc.pluginStep(b, st, sp, name)
	switch {
	case err != nil || ok:
	case st.Kind == ir.StepAction || st.Action != nil || len(st.With) > 0:
		m, err = tc.actionStep(b, st, sp, name)
	case st.Kind == ir.StepScript || st.Kind == "":
		m, err = tc.scriptStep(b, st, sp, name)
	default:
		why := "step kind " + string(st.Kind) + " has no Tekton form"
		tc.e.lose(sp, caps.StepAction, why)
		tc.e.comment(sp, "step "+sp+" has kind "+string(st.Kind))
		m, err = tc.failedAction(b, st, sp, name, why)
	}
	if m != nil && comment != "" {
		m.N.HeadComment = comment
	}
	return m, err
}

// scriptStep writes a script step. An expression with no spelling comments out its line.
func (tc *taskCtx) scriptStep(b *stepBook, st *ir.Step, sp, name string) (*wmap, error) {
	sc := newScope()
	m := newMap()
	m.SetStr("name", name)
	if st.Name != "" {
		m.SetStr("displayName", st.Name)
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
		m.SetStr("image", image)
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
		if strings.HasPrefix(shebangShell(st.Shell), envShebang) {
			tc.e.emulate(sp, caps.StepShell, "env shebang", "the script starts with #!"+envShebang+st.Shell+"; the image must provide "+st.Shell+", else the step stops with env: can't execute '"+st.Shell+"' and exit code 127")
		}
		lines = append([]string{shebang + shebangShell(st.Shell)}, lines...)
	} else if len(lines) > 0 && tc.bashDefault(st) {
		tc.e.emulate(sp, caps.StepShell, "bash-if-present preamble in the script", "the source runs the step under bash when the image has it; the script starts with a line that re-runs it under bash -e when the image has bash, else it runs under sh -e, so bash-only syntax fails under sh")
		lines = append(bashPreamble(), lines...)
	}
	if len(lines) > 0 && !tc.commandForm(m, st, sp, guard) {
		m.Set("script", block(strings.Join(lines, "\n")))
	}
	if len(userEnv)+len(sc.env) > 0 {
		m.Set("env", seqOf(append(userEnv, sc.env...)...))
	}
	if orig, ok := tc.sourceWorkDir(st); ok {
		m.SetStr("workingDir", orig)
	} else if st.WorkDir != "" {
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
			m.SetStr("workingDir", wd)
		}
	}
	tc.stepTail(m, st, cond)
	return m, tc.stepExt(m, st)
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
		m.SetStr("timeout", tc.durationText(st.Timeout))
	}
	if st.ContinueOnError {
		m.SetStr("onError", onErrorContinue)
	}
	if len(cond.when) > 0 {
		m.Set("when", whenNode(cond.when))
	}
}

// stepExt writes the Tekton Ext keys of a step that came from Tekton.
func (tc *taskCtx) stepExt(m *wmap, st *ir.Step) error {
	x := extOf(st.Ext)
	if len(x) == 0 || !tc.e.own {
		return nil
	}
	skip := map[string]bool{ExtWorkspaceDir: true, ExtWorkspaceDirIR: true, ExtScriptSource: true, ExtScriptIR: true, ExtSourceCheckout: true, ExtWorkspaceDirs: true, ExtCommand: true, ExtArgs: true, ExtScriptLine: true, ExtBashDefault: true, "ref": true, "params": true, "taskRef": true, "timeout": true, "onError": true}
	for _, k := range sortedKeys(x) {
		if skip[k] || m.Has(k) {
			continue
		}
		n, err := plainNode(x[k])
		if err != nil {
			return err
		}
		m.Set(k, n)
	}
	for _, k := range []string{"timeout", "onError"} {
		if v, ok := x[k]; ok {
			n, err := plainNode(v)
			if err != nil {
				return err
			}
			m.Set(k, n)
		}
	}
	return nil
}

// scriptLines converts the script of a step. A line with no spelling is commented out with the rest of its
// statement, and a failing line takes its place so the step stops where the source would have run it.
func (tc *taskCtx) scriptLines(sc *stepScope, st *ir.Step, sp string) ([]string, error) {
	script := st.Script
	if x := extOf(st.Ext); tc.e.own && x[ExtScriptIR] == strings.Join(script, "\n") {
		switch src := x[ExtScriptSource].(type) {
		case []string:
			script = src
		case []any:
			script = make([]string, 0, len(src))
			for _, l := range src {
				s, _ := l.(string)
				script = append(script, s)
			}
		}
	}
	outs := make([]string, len(script))
	why := make([]string, len(script))
	lost := make([]bool, len(script))
	for i, l := range script {
		var err error
		if outs[i], why[i], err = tc.scriptLine(sc, sp, l); err != nil {
			return nil, err
		}
		lost[i] = why[i] != ""
	}
	spans := emitkit.CommentSpans(script, lost)
	if len(spans) == 0 {
		return outs, nil
	}
	var out []string
	next := 0
	for i := 0; i < len(script); i++ {
		if next < len(spans) && spans[next].From == i {
			span := spans[next]
			next++
			first := ""
			for k := span.From; k <= span.To; k++ {
				out = append(out, "# pipemaster:lost script.interpolation: "+commentSafe(script[k]))
				if first == "" {
					first = why[k]
				}
			}
			detail := emitkit.SpanDetail(first, span.To-span.From+1, span.Open)
			tc.e.lose(sp, caps.ScriptInterpolation, detail)
			out = append(out, emitkit.FailLoud(caps.ScriptInterpolation, sp, detail))
			i = span.To
			continue
		}
		out = append(out, outs[i])
	}
	return out, nil
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

// bashPreamble is the first lines of a script that runs under bash -e when the image has bash, else under sh -e.
// The variable stops the second run from looping.
func bashPreamble() []string {
	return []string{
		shebang + "/bin/sh",
		`if [ -z "${PIPEMASTER_BASH:-}" ] && command -v bash >/dev/null 2>&1; then PIPEMASTER_BASH=1 exec bash -e "$0"; fi`,
		"set -e",
	}
}

// bashDefault is true when a step with no shell runs under bash-if-present: the source default is bash, or a
// Tekton file held the preamble.
func (tc *taskCtx) bashDefault(st *ir.Step) bool {
	if strings.TrimSpace(st.Shell) != "" {
		return false
	}
	if b, _ := extOf(st.Ext)[ExtBashDefault].(bool); b {
		return true
	}
	return !tc.e.own && provider.BashDefault(tc.e.origin)
}

// commandForm writes command and args back for a step that came as command and args, while its line is as
// the parser wrote it. A step that changed becomes a script and reports that a shell must exist in the image.
func (tc *taskCtx) commandForm(m *wmap, st *ir.Step, sp string, guard expr.Expr) bool {
	x := extOf(st.Ext)
	if !tc.e.own || x[ExtCommand] == nil {
		return false
	}
	line, _ := x[ExtScriptLine].(string)
	if guard != nil || st.Shell != "" || len(st.Script) == 0 || strings.Join(st.Script, "\n") != line {
		tc.e.emulate(sp, caps.StepScript, "script step", "command and args are written as a script, so the image needs a shell")
		return false
	}
	for _, k := range []string{ExtCommand, ExtArgs} {
		if v, ok := x[k]; ok {
			if n := tc.e.node(sp, k, v); n != nil {
				m.Set(k, n)
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

// sourceWorkDir returns the working directory text that the file held, while the IR directory is as parsed.
func (tc *taskCtx) sourceWorkDir(st *ir.Step) (string, bool) {
	x := extOf(st.Ext)
	orig, ok := x[ExtWorkspaceDir].(string)
	rel, _ := x[ExtWorkspaceDirIR].(string)
	return orig, tc.e.own && ok && rel == st.WorkDir
}

const (
	envShebang = "/usr/bin/env "
	// shellSh keeps its env form in the IR: Argo runs sh as its default command, so a bare sh would not come back.
	shellSh = "sh"
)

// shebangShell names the interpreter of a script. The kernel needs a path, so a bare shell name such as bash
// goes through env.
func shebangShell(shell string) string {
	if strings.HasPrefix(shell, "/") || strings.ContainsAny(shell, " \t") {
		return shell
	}
	return envShebang + shell
}
