package gha

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// pluginEnvPrefix starts the variable a plugin reads each setting from.
const pluginEnvPrefix = "PLUGIN_"

const (
	defaultEntrypoint = "/bin/sh"
	shellBash         = "bash"
	shellSh           = "sh"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./=:@%+,-]+$`)

func stepPath(jp, sid string) string { return jp + "/step/" + sid }

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	var sb strings.Builder
	sb.WriteByte('\'')
	for rest := s; rest != ""; {
		start := strings.Index(rest, "${{")
		end := -1
		if start >= 0 {
			end = strings.Index(rest[start:], "}}")
		}
		if start < 0 || end < 0 {
			sb.WriteString(strings.ReplaceAll(rest, "'", `'\''`))
			break
		}
		end += start + 2
		// GHA evaluates an expression before the shell runs, so its own quotes must stay as written.
		sb.WriteString(strings.ReplaceAll(rest[:start], "'", `'\''`))
		sb.WriteString(rest[start:end])
		rest = rest[end:]
	}
	sb.WriteByte('\'')
	return sb.String()
}

// retryScript wraps body (attempts is the total number of runs, not the number of retries) in a loop that runs it up to attempts times; set -e inside the subshell
// still applies because the subshell is not an if or until condition.
func retryScript(body []string, attempts int) []string {
	out := []string{
		fmt.Sprintf("for pipemaster_try in $(seq 1 %d); do", attempts),
		fmt.Sprintf(`  echo "pipemaster: attempt ${pipemaster_try} of %d" >&2`, attempts),
		"  set +e",
		"  (",
		"  set -e",
	}
	out = append(out, body...)
	return append(out,
		"  )",
		"  pipemaster_rc=$?",
		"  set -e",
		`  [ "$pipemaster_rc" -eq 0 ] && exit 0`,
		"done",
		`exit "$pipemaster_rc"`,
	)
}

// derivedStep reports whether any provider made the step ID up from the step position; such an ID is never written back as a name.
func derivedStep(st *ir.Step, own map[string]any) bool {
	if d, _ := own[ExtDerivedID].(bool); d {
		return true
	}
	for _, raw := range st.Ext {
		if m, ok := raw.(map[string]any); ok && m[ExtDerivedID] == true {
			return true
		}
	}
	return false
}

// reidentify names a step that another provider gave a made-up ID by the ID a reader derives for its place in
// this file, so its markers and report entries match the step after a return parse.
func (e *emitter) reidentify(j *ir.Job, st *ir.Step, pos int) *ir.Step {
	if own, _ := e.ext(st.Ext)[ExtDerivedID].(bool); own || !derivedStep(st, nil) {
		return st
	}
	c := *st
	c.ID = derivedID(j.ID, pos)
	return &c
}

// noopScript runs where the source holds no work: a job with no steps, a workflow with no jobs, or a script step
// with no lines. It only keeps the file valid. Source work with no GHA form uses failLoud instead.
const noopScript = "true"

// failLoud makes the run text of a step that stands in for source work with no GHA form.
func failLoud(f caps.Feature, sp, why string) *yaml.Node { return str(emitkit.FailLoud(f, sp, why)) }

func retryable(shell string) bool { return shell == "" || shell == shellBash || shell == shellSh }

// stepNode builds one step; attempts > 0 wraps a script step in the retry loop.
func (e *emitter) stepNode(jp string, st *ir.Step, attempts int) (*yaml.Node, bool, error) {
	sp := stepPath(jp, st.ID)
	ext := e.ext(st.Ext)
	e.foreignExt(sp, st.Ext)
	derived := derivedStep(st, ext)
	m := newMap()
	if !derived && st.ID != "" {
		id := st.ID
		if w, ok := e.stepIDs[id]; ok {
			id = w
		}
		m.set("id", str(id))
	}
	if st.Name != "" {
		t, err := e.template(sp, st.Name)
		if err != nil {
			return nil, false, err
		}
		m.set("name", str(t))
	}
	var usesText string
	usesOK := true
	if (st.Kind == ir.StepAction || st.Kind == ir.StepPipelineCall) && st.Action != nil && st.Image == nil {
		var uerr error
		if usesText, usesOK, uerr = e.uses(sp, st.Action); uerr != nil {
			return nil, false, uerr
		}
	}
	var markers []string
	dockerOK := st.Image == nil || !strings.Contains(st.Image.Ref, "$")
	switch {
	case !usesOK:
		e.lose(sp, caps.StepIf, whyDisabled)
		m.set("if", boolNodeOf(false))

	case st.If != nil:
		m.set("if", e.condition(sp, caps.StepIf, st.If, "step"))
	}
	wrapped := false
	var err error
	runOK := false
	if !dockerOK {
		var err error
		if runOK, err = e.dockerRunStep(m, sp, st); err != nil {
			return nil, false, err
		}
	}
	switch {
	case runOK:
		markers = append(markers, emitkit.Marker(caps.StepImage, sp))
	case !dockerOK && len(st.Script) == 0:
		why := "a docker step needs a literal image reference and the reference holds a variable"
		e.lose(sp, caps.StepImage, why+"; the step fails with a message")
		m.set("run", failLoud(caps.StepImage, sp, why))
	case !dockerOK:
		e.lose(sp, caps.StepImage, "a docker step needs a literal image reference and the reference holds a variable; the script runs on the runner")
		lines := e.withoutRemoteRefs(sp, st.Script)
		var ts []string
		if ts, err = e.templates(sp, lines); err == nil {
			m.set("run", block(ts))
		}
	case (st.Kind == ir.StepScript || st.Kind == ir.StepPlugin) && st.Image != nil:
		markers = append(markers, emitkit.Marker(caps.StepImage, sp))
		err = e.imageStep(m, sp, st)
	case st.Kind == ir.StepScript && len(st.Script) > 0:
		lines := e.withoutRemoteRefs(sp, st.Script)
		if len(lines) == 0 {
			lines = []string{emitkit.FailLoud(caps.StepScript, sp, "every line of the script is a remote reference with no GHA form")}
		}
		if attempts > 0 && retryable(st.Shell) {
			lines, wrapped = retryScript(lines, attempts), true
			markers = append(markers, emitkit.Marker(caps.JobRetry, jp))
		}
		var ts []string
		if ts, err = e.templates(sp, lines); err == nil {
			m.set("run", block(ts))
		}
	case (st.Kind == ir.StepAction || st.Kind == ir.StepPipelineCall) && st.Action != nil:
		if !usesOK {
			m.set("run", failLoud(caps.StepAction, sp, whyDisabled))
			break
		}
		m.set("uses", str(usesText))
		err = e.withKey(m, sp, st.With, nil)
	case st.Kind == ir.StepScript && st.Image == nil:
		m.set("run", str(noopScript))
	case st.Kind == ir.StepAction && st.Action == nil:
		why := "the step names no action GHA can run"
		e.lose(sp, caps.StepAction, why+"; the step fails with a message")
		m.set("run", failLoud(caps.StepAction, sp, why))
	case st.Kind == ir.StepTask:
		why := "a task reference has no GHA form"
		e.lose(sp, caps.StepAction, why+"; the step fails with a message")
		m.set("run", failLoud(caps.StepAction, sp, why))
	default:
		why := "a step of kind " + string(st.Kind) + " has no GHA form"
		e.lose(sp, caps.StepAction, why+"; the step fails with a message")
		m.set("run", failLoud(caps.StepAction, sp, why))
	}
	if err != nil {
		return nil, false, err
	}
	if err := e.stepTail(m, sp, st, ext); err != nil {
		return nil, false, err
	}
	m.n.HeadComment = strings.Join(append(markers, e.drain()...), "\n")
	return m.n, wrapped, nil
}

// withoutResultWrites points the result files of Tekton at the null device, because no shell outside Tekton can
// run the substitution.
func (e *emitter) withoutResultWrites(sp string, lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, emitkit.NeutralizeResultWrite(e.lose, sp, l))
	}
	return out
}

// remoteRefPrefix starts a script line that stands for a GitLab !reference into a remote include.
const remoteRefPrefix = "!reference ["

// withoutRemoteRefs drops the lines that only a GitLab remote include could fill; the metadata keeps them.
func (e *emitter) withoutRemoteRefs(sp string, lines []string) []string {
	var out []string
	for _, l := range e.withoutResultWrites(sp, lines) {
		if strings.HasPrefix(l, remoteRefPrefix) {
			e.lose(sp, caps.StepScript, "the line "+l+" depends on a remote GitLab include and has no GHA form")
			continue
		}
		out = append(out, l)
	}
	return out
}

func (e *emitter) withKey(m *omap, sp string, w map[string]ir.Value, first *omap) error {
	if len(w) == 0 && (first == nil || first.empty()) {
		return nil
	}
	n, err := e.withNode(sp, w)
	if err != nil {
		return err
	}
	out := newMap()
	if first != nil {
		for i := 0; i+1 < len(first.n.Content); i += 2 {
			out.set(first.n.Content[i].Value, first.n.Content[i+1])
		}
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out.set(n.Content[i].Value, n.Content[i+1])
	}
	m.set("with", out.n)
	return nil
}

// shellName writes a shell path as the shell name that GHA accepts, and reports the path as lost.
func (e *emitter) shellName(sp, shell string) string {
	i := strings.LastIndex(shell, "/")
	if i < 0 || strings.Contains(shell, "{0}") {
		return shell
	}
	base := shell[i+1:]
	if base != shellBash && base != shellSh {
		return shell
	}
	e.lose(sp, caps.StepShell, "GHA names a shell without its path; "+shell+" runs as "+base)
	return base
}

func (e *emitter) stepTail(m *omap, sp string, st *ir.Step, ext map[string]any) error {
	if st.Shell == "" && st.Image == nil && len(st.Script) > 0 && m.has("run") && provider.BashDefault(e.origin) && !provider.SameFamily(e.origin, e.d.ID) {
		// A runner without a shell setting may pick sh, and the source ran the script under bash.
		m.set("shell", str(shellBash))
		e.mark(caps.StepShell, sp)
	}
	if st.Shell != "" {
		if st.Image == nil {
			m.set("shell", str(e.shellName(sp, st.Shell)))
		} else {
			e.lose(sp, caps.StepShell, "a step that runs in an image has no shell setting")
		}
	}
	switch {
	case st.WorkDir != "" && st.Image != nil && m.has("uses"):
		e.emulate(sp, caps.StepWorkdir, "kept in the image step marker", "a docker step has no working-directory")
	case st.WorkDir != "":
		t, err := e.template(sp, st.WorkDir)
		if err != nil {
			return err
		}
		m.set("working-directory", str(t))
	}
	env, err := e.vars(sp, st.Env)
	if err != nil {
		return err
	}
	if e.pluginVars != nil {
		env.Content = append(env.Content, e.pluginVars.Content...)
		e.pluginVars = nil
	}
	if len(env.Content) > 0 {
		m.set("env", env)
	}
	if st.ContinueOnError {
		m.set("continue-on-error", boolNodeOf(true))
	}
	if st.Timeout != nil {
		m.set("timeout-minutes", minutesNode(*st.Timeout))
	}
	skip := map[string]bool{ExtDerivedID: true}
	if st.Kind == ir.StepPipelineCall {
		skip["secrets"] = true
	}
	return m.addExtra(ext, skip)
}

// imageStep runs a step in its own image through a docker:// action; the script runs under sh -ec
// when the image names no entrypoint, otherwise it becomes the entrypoint's last argument.
// imageParts is what an image step writes: the image reference, the entrypoint and the args, the last two empty
// when the step has no script and no entrypoint.
func (e *emitter) imageParts(sp string, st *ir.Step) (ref, entrypoint, args string, err error) {
	if ref, err = e.template(sp, st.Image.Ref); err != nil {
		return
	}
	ts, err := e.templates(sp, e.withoutResultWrites(sp, st.Script))
	if err != nil {
		return
	}
	script := strings.Join(ts, "\n")
	ep := st.Image.Entrypoint
	switch {
	case len(ep) > 0 && ep[0] != "":
		entrypoint = ep[0]
		words := make([]string, 0, len(ep))
		for _, w := range ep[1:] {
			words = append(words, shellQuote(w))
		}
		if script != "" {
			words = append(words, shellQuote(script))
		}
		args = strings.Join(words, " ")
	case script != "":
		entrypoint, args = defaultEntrypoint, "-ec "+shellQuote(script)
	}
	return
}

func (e *emitter) imageStep(m *omap, sp string, st *ir.Step) error {
	ref, entrypoint, argText, err := e.imageParts(sp, st)
	if err != nil {
		return err
	}
	m.set("uses", str(dockerPrefix+ref))
	args := newMap()
	if entrypoint != "" {
		args.set("entrypoint", str(entrypoint))
	}
	if argText != "" {
		args.set("args", str(argText))
	}
	arg := imageArg{Image: *st.Image, Script: st.Script, Kind: st.Kind, WorkDir: st.WorkDir}
	with := st.With
	if st.Kind == ir.StepPlugin && len(with) > 0 {
		vars, names, err := e.pluginEnv(sp, st.With)
		if err != nil {
			return err
		}
		e.pluginVars, arg.Settings, with = vars, names, nil
		e.emulate(sp, caps.StepImage, "settings as PLUGIN_ environment variables", "the plugin settings are passed as PLUGIN_<NAME> variables, the Woodpecker convention; a GHA docker action reads its inputs as INPUT_<NAME>")
	}
	if err := e.markArg(caps.StepImage, sp, arg); err != nil {
		return err
	}
	return e.withKey(m, sp, with, args)
}

// pluginEnvName is the variable a plugin reads a setting from.
func pluginEnvName(setting string) string {
	return pluginEnvPrefix + strings.Map(func(r rune) rune {
		if r == '-' || r == '.' {
			return '_'
		}
		return r
	}, strings.ToUpper(setting))
}

// pluginEnv writes the settings of a plugin step as the PLUGIN_ variables it reads, and returns the names of
// the settings it could write.
func (e *emitter) pluginEnv(sp string, with map[string]ir.Value) (env *yaml.Node, names []string, err error) {
	env = newMap().n
	owner := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(with)) {
		n, err := e.value(sp, with[k])
		if err != nil {
			return nil, nil, err
		}
		if n != nil {
			name := pluginEnvName(k)
			if first, dup := owner[name]; dup {
				e.lose(sp, caps.StepImage, "the setting "+k+" maps to "+name+", which the setting "+first+" already sets; "+k+" is dropped")
				continue
			}
			owner[name] = k
			env.Content = append(env.Content, str(name), n)
			names = append(names, k)
		}
	}
	return env, names, nil
}

// imageArg is the marker argument of an image step: what the step ran before it became a docker action.
type imageArg struct {
	Kind     ir.StepKind `json:"kind,omitempty"`
	WorkDir  string      `json:"workdir,omitempty"`
	Image    ir.Image    `json:"image"`
	Script   []string    `json:"script,omitempty"`
	Settings []string    `json:"settings,omitempty"`
	Passed   []string    `json:"passed,omitempty"`
	Run      bool        `json:"run,omitempty"`
}

// jobSteps lists a job's steps with the emulation steps around them: downloads and cache first, uploads last.
func (e *emitter) jobSteps(c jobCtx, j *ir.Job) (*yaml.Node, error) {
	jp := jobPath(c.pl.ID, j.ID)
	seq := seqOf()
	pre, err := e.preSteps(c, jp, j)
	if err != nil {
		return nil, err
	}
	seq.Content = append(seq.Content, pre...)
	attempts, wrapped := 0, 0
	if j.Retry != nil {
		attempts = max(j.Retry.Attempts, 1)
		if len(j.Retry.On) > 0 {
			e.emulate(jp, caps.JobRetry, "step-level retry loop via shell", "retry conditions are ignored; every failure retries")
		}
	}
	e.stepIDs = stepIDMap(j)
	e.passEnv = append(varNames(c.pl.Env), varNames(j.Env)...)
	for _, st := range j.Steps {
		if st == nil {
			continue
		}
		n, w, err := e.stepNode(jp, e.reidentify(j, st, len(seq.Content)+1), attempts)
		if err != nil {
			return nil, err
		}
		if w {
			wrapped++
		} else if attempts > 0 && st.Kind == ir.StepScript {
			e.lose(jp, caps.JobRetry, "step "+st.ID+" uses shell "+st.Shell+", which the retry loop cannot wrap")
		}
		seq.Content = append(seq.Content, n)
	}
	if j.Retry != nil && wrapped == 0 {
		e.lose(jp, caps.JobRetry, "the job has no bash or sh script step to retry")
	}
	if j.Retry != nil && wrapped > 0 {
		if err := e.markArg(caps.JobRetry, jp, j.Retry); err != nil {
			return nil, err
		}
	}
	post, err := e.postSteps(jp, j)
	if err != nil {
		return nil, err
	}
	seq.Content = append(seq.Content, post...)
	if err := e.markCarried(jp, j, len(pre), len(post)); err != nil {
		return nil, err
	}
	if len(seq.Content) == 0 {
		// A trigger job or an override of an included job has no script, and a GHA job needs a step.
		n := newMap()
		n.set("run", str(noopScript))
		seq.Content = append(seq.Content, n.n)
		e.lose(stepPath(jp, "script"), caps.StepScript, "the job has no steps; a no-op step keeps the workflow valid")
		e.mark(caps.StepScript, jp)
	}
	return seq, nil
}
