package concourse

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// kindKeys are the keys that name the kind of a step.
var kindKeys = []string{"get", "put", "task", "run", "set_pipeline", "load_var", "try", "do", "in_parallel"}

var (
	wholeVar = regexp.MustCompile(`^\(\(([A-Za-z_][A-Za-z0-9_-]*)\)\)$`)
	localVar = regexp.MustCompile(`^\(\(\.:([A-Za-z_][A-Za-z0-9_]*)\)\)$`)
	identVar = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	shells   = map[string]bool{"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true}
)

// step reads one plan step. A step that the IR cannot model becomes one plugin step that keeps it verbatim.
func (jb *jobBuilder) step(raw any) []*ir.Step {
	m, ok := asMap(raw)
	if !ok {
		return []*ir.Step{jb.opaque(raw, "step", "a plan entry is not a mapping", 0)}
	}
	if _, has := m.Vals["across"]; has {
		return []*ir.Step{jb.opaque(m, "across", "across is a matrix over a step", m.Lines["across"])}
	}
	if gets, ok := parallelGets(m); ok && len(gets) > 0 {
		if co := jb.checkoutStep(m, gets, jb.used); co != nil {
			return []*ir.Step{co}
		}
	}
	var kinds []string
	for _, key := range kindKeys {
		if _, has := m.Vals[key]; has {
			kinds = append(kinds, key)
		}
	}
	if len(kinds) != 1 {
		return []*ir.Step{jb.opaque(m, "step", "the step has no single kind key", 0)}
	}
	switch kinds[0] {
	case "get", "put":
		return []*ir.Step{jb.resourceStep(m, kinds[0])}
	case "task":
		return []*ir.Step{jb.taskStep(m)}
	case "try":
		return jb.tryStep(m)
	}
	return []*ir.Step{jb.opaque(m, kinds[0], kinds[0]+" has no IR step form", m.Lines[kinds[0]])}
}

// uniqueID returns name, or name with a number when an earlier step of the job has it.
func (jb *jobBuilder) uniqueID(name string) string {
	if name == "" {
		name = "step"
	}
	jb.ids[name]++
	jb.b.present[jb.path+"/step/"+name] = true
	if n := jb.ids[name]; n > 1 {
		id := fmt.Sprintf("%s-%d", name, n)
		jb.ids[id]++
		jb.b.present[jb.path+"/step/"+id] = true
		jb.k.Rep.Add(jb.entry(caps.ExtPassthrough, caps.Emulated, "step id made unique", fmt.Sprintf("step name %s is used again in the job; the id is %s and the name stays", name, id), 0))
		return id
	}
	return name
}

// named makes a step whose ID is the task or step name. The Name is set only when the ID differs, because a Name
// equal to the ID is no display name and would not survive a provider whose step name is its ID.
func (jb *jobBuilder) named(name string) *ir.Step {
	st := &ir.Step{ID: jb.uniqueID(name)}
	if st.ID != name {
		st.Name = name
	}
	return st
}

func (jb *jobBuilder) opaque(val any, kind, why string, line int) *ir.Step {
	name := strings.ReplaceAll(kind, "_", "-")
	if m, ok := asMap(val); ok {
		if n, ok := text(m.Vals[kind]); ok && (kind == "load_var" || kind == "set_pipeline") {
			name = n
		}
	}
	st := jb.named(name)
	st.Kind = ir.StepPlugin
	st.Ext = ir.Ext{dialect: map[string]any{ExtKind: kind, ExtStep: plain(val)}}
	feature := caps.ExtPassthrough
	if kind == "across" {
		feature = caps.JobMatrix
	}
	jb.k.Note(feature, fmt.Sprintf("step %s: %s; the step is kept verbatim in Ext and the IR cannot run it", name, why), line)
	return st
}

// resourceStep keeps a get or put as a resource step; the whole configuration stays in Ext.
func (jb *jobBuilder) resourceStep(m *omap, kind string) *ir.Step {
	name, _ := text(m.Vals[kind])
	res := resourceName(m)
	st := jb.named(name)
	st.Kind = ir.StepResource
	k := newSink(jb.k.Rep, jb.b.file, jb.path+"/step/"+st.ID)
	r := newReader(m)
	jb.timeout(st, r)
	typ := ""
	if rr := jb.b.res[res]; rr != nil {
		typ = rr.typ
	}
	keepAll(k, r, caps.StepScript, fmt.Sprintf("%s step %s on resource %s (type %s) has no script; its configuration is kept in Ext", kind, name, res, typ), m.Lines[kind])
	st.Ext = k.Ext()
	return st
}

func (jb *jobBuilder) timeout(st *ir.Step, r *reader) {
	if v, ok := r.Take(keyTimeout); ok {
		if d, ok := parseDuration(v); ok {
			st.Timeout = &d
			return
		}
		r.Release(keyTimeout)
	}
}

// tryStep reads try as the inner step with ContinueOnError. A try with other keys is kept verbatim.
func (jb *jobBuilder) tryStep(m *omap) []*ir.Step {
	inner := m.Vals["try"]
	rest := 0
	for _, key := range m.Order {
		if key != "try" && key != keyTimeout {
			rest++
		}
	}
	if _, isMap := asMap(inner); !isMap || rest > 0 {
		return []*ir.Step{jb.opaque(m, "try", "try has keys beside the step it wraps", m.Lines["try"])}
	}
	steps := jb.step(inner)
	for _, s := range steps {
		s.ContinueOnError = true
	}
	if v, ok := m.Vals[keyTimeout]; ok {
		d, ok := parseDuration(v)
		if !ok || len(steps) != 1 || steps[0].Timeout != nil {
			return []*ir.Step{jb.opaque(m, "try", "the try timeout has no place in the IR", m.Lines[keyTimeout])}
		}
		steps[0].Timeout = &d
	}
	return steps
}

func (jb *jobBuilder) taskStep(m *omap) *ir.Step {
	name, ok := text(m.Vals["task"])
	cfg, hasCfg := asMap(m.Vals["config"])
	file, hasFile := text(m.Vals["file"])
	if !ok || hasCfg == hasFile {
		return jb.opaque(m, "task", "the task has no name or not exactly one of config and file", m.Lines["task"])
	}
	st := jb.named(name)
	written := st.ID
	k := newSink(jb.k.Rep, jb.b.file, jb.path+"/step/"+st.ID)
	r := newReader(m)
	r.Take("task")
	form := hasCfg && jb.workspaceForm(m, cfg)
	jb.timeout(st, r)
	var cfgParams *omap
	if hasFile {
		r.Take("file")
		k.Out["file"] = file
		var resolved bool
		cfgParams, resolved = jb.fileTask(st, k, m, file, written, m.Lines["file"])
		jb.fresh[st] = resolved
	} else {
		r.Take("config")
		st.Kind = ir.StepScript
		cfgParams = jb.config(st, k, cfg, m.Lines["config"], form)
	}
	jb.stepImage(st, k, r)
	if hasFile && st.Kind == ir.StepScript {
		k.Out[ExtTaskDigest] = taskDigest(st, refOf(st.Image))
		if cfgParams != nil && len(cfgParams.Order) > 0 {
			k.Out[ExtFileParams] = paramList(cfgParams)
		}
	}
	jb.env(st, k, cfgParams, r)
	k.KeepRest(r)
	st.Ext = k.Ext()
	st = jb.heldAction(m, written, st)
	if st.Kind == ir.StepScript {
		jb.applyMarkers(m, st, written, form)
	}
	jb.written[st] = written
	return st
}

// applyMarkers reads the markers of a task, which the emitter keyed by the written task name.
func (jb *jobBuilder) applyMarkers(m *omap, st *ir.Step, written string, form bool) {
	st.ID = written
	jb.guardLines(st)
	jb.workdirMarker(st, form)
	jb.secretMarker(st)
	jb.nameMarker(st)
	jb.matrixBack(st)
	jb.carryStep(m, written, st)
	jb.taken[st.ID] = true
}

// config reads an inline task config into the step. It returns the config params, which the step env reads.
func (jb *jobBuilder) config(st *ir.Step, k *sink, cfg *omap, line int, form bool) *omap {
	r := newReader(cfg)
	if form {
		r.Take("inputs")
		r.Take("outputs")
	}
	params, _ := asMap(takeVal(r, "params"))
	if run, ok := asMap(takeVal(r, "run")); ok {
		jb.run(st, k, run, line)
	} else {
		jb.noRun(k, line)
	}
	if v, ok := r.Take("image_resource"); ok {
		jb.image(st, k, v, r.Line("image_resource"))
	}
	if paths, ok := cachePaths(cfg.Vals["caches"]); ok && len(paths) > 0 && slices.Equal(paths, jb.caches) {
		r.Take("caches")
	}
	rest := omapNew()
	for _, key := range r.Rest() {
		rest.Set(key, cfg.Vals[key], cfg.Lines[key])
	}
	if len(rest.Order) > 0 {
		k.Keep(ExtConfig, rest, caps.ExtPassthrough, "task config keys "+strings.Join(rest.Order, ", ")+" have no IR field; kept in Ext", line)
	}
	return params
}

// paramList is the params of a task file as ordered name and value pairs, the form that Ext and the marker hold.
func paramList(p *omap) []any {
	out := make([]any, 0, len(p.Order))
	for _, name := range p.Order {
		v, _ := text(p.Vals[name])
		out = append(out, map[string]any{"name": name, "value": v})
	}
	return out
}

func takeVal(r *reader, key string) any {
	v, _ := r.Take(key)
	return v
}

func (jb *jobBuilder) noRun(k *sink, line int) {
	k.Out[ExtEntrypoint] = true
	k.Note(caps.StepScript, "the task config has no run.path, so the image entrypoint runs; the IR script is empty", line)
}

// run reads run.path, run.args and run.dir. A shell -c form gives the shell and the script lines; any other
// command becomes one quoted script line with the command and args kept in Ext.
func (jb *jobBuilder) run(st *ir.Step, k *sink, run *omap, line int) {
	if runHasVar(run) {
		k.Keep(ExtRun, run, caps.StepScript, "run holds a ((var)), which no script can carry; run is kept in Ext and the step runs nothing", line)
		return
	}
	r := newReader(run)
	if d, ok := textOf(r.Take("dir")); ok {
		st.WorkDir = d
	} else {
		r.Release("dir")
	}
	cmd, hasPath := textOf(r.Take("path"))
	argv, argsOK := []string(nil), true
	if v, has := r.Take("args"); has {
		argv, argsOK = stringList(v)
	}
	switch {
	case !hasPath:
		r.Release("path")
		jb.noRun(k, line)
	case !argsOK:
		r.Release("path")
		r.Release("args")
		k.Note(caps.StepScript, "run.args is not a list of strings; run is kept in Ext", line)
	default:
		jb.command(st, k, cmd, argv, line)
	}
	if rest := r.Rest(); len(rest) > 0 {
		kept := omapNew()
		for _, key := range rest {
			kept.Set(key, run.Vals[key], run.Lines[key])
		}
		k.Keep(ExtRun, kept, caps.ExtPassthrough, "run keys "+strings.Join(rest, ", ")+" have no IR field; kept in Ext", line)
	}
}

// runHasVar reports a ((var)) in run.path, run.args or run.dir. Concourse resolves it at run time and another
// provider would run the literal text, so the parser leaves it out of the script.
func runHasVar(run *omap) bool {
	for _, key := range []string{"path", "args", "dir"} {
		if l, ok := textList(run.Vals[key]); ok {
			for _, w := range l {
				if strings.Contains(w, varOpen) {
					return true
				}
			}
		}
	}
	return false
}

func (jb *jobBuilder) command(st *ir.Step, k *sink, cmd string, argv []string, line int) {
	if isPowershell(cmd) && len(argv) > 1 && argv[len(argv)-2] == "-Command" {
		st.Shell = cmd
		for _, l := range strings.Split(strings.TrimSuffix(argv[len(argv)-1], "\n"), "\n") {
			st.Script = append(st.Script, escapeLine(l))
		}
		k.Out[ExtShellFlags] = plainStrings(argv[:len(argv)-1])
		k.Note(caps.StepShell, fmt.Sprintf("shell flags %v are kept in Ext; the IR holds the shell and the script", argv[:len(argv)-1]), line)
		return
	}
	if cmd == shellCommand && len(argv) == 4 && argv[0] == "-c" && argv[1] == provider.BashOrShScript && argv[2] == provider.BashOrShArgv0 {
		for _, l := range strings.Split(strings.TrimSuffix(argv[3], "\n"), "\n") {
			st.Script = append(st.Script, escapeLine(l))
		}
		k.Out[ExtBashDefault] = true
		return
	}
	if shells[path.Base(cmd)] && len(argv) > 1 {
		flags, script := argv[:len(argv)-1], argv[len(argv)-1]
		if shellFlags(flags) {
			st.Shell = cmd
			for _, l := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
				st.Script = append(st.Script, escapeLine(l))
			}
			if cmd == shellCommand && len(flags) == 1 && flags[0] == defaultFlags {
				st.Shell = ""
				return
			}
			k.Out[ExtShellFlags] = plainStrings(flags)
			k.Note(caps.StepShell, fmt.Sprintf("shell flags %v are kept in Ext; the IR holds the shell and the script", flags), line)
			return
		}
	}
	words := []string{cmd}
	words = append(words, argv...)
	quoted := make([]string, len(words))
	for i, w := range words {
		q, err := emitkit.ShellWord([]expr.Part{{Text: w}})
		if err != nil {
			q = w
		}
		quoted[i] = q
	}
	st.Script = []string{strings.Join(quoted, " ")}
	k.Out[ExtCommand] = cmd
	k.Out[ExtArgs] = plainStrings(argv)
	k.Out[ExtScriptLine] = st.Script[0]
	k.Note(caps.StepScript, "run.path and run.args are written as one script line; both are kept in Ext", line)
}

// shellFlags reports whether flags are all options and one of them is -c.
func shellFlags(flags []string) bool {
	hasC := false
	for _, f := range flags {
		if !strings.HasPrefix(f, "-") || strings.HasPrefix(f, "--") {
			return false
		}
		hasC = hasC || strings.Contains(f, "c")
	}
	return hasC
}

func escapeLine(l string) string {
	s, err := expr.PrintScriptTemplate([]expr.Part{{Text: l}})
	if err != nil {
		return l
	}
	return s
}

func plainStrings(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// image reads image_resource as an image reference when it is a registry-image with a literal repository and tag
// and no other key; anything else stays in Ext.
func (jb *jobBuilder) image(st *ir.Step, k *sink, v any, line int) {
	if ref, ok := imageRef(v, jb.inFile); ok {
		st.Image = &ir.Image{Ref: ref}
		return
	}
	k.Keep(ExtImageResource, v, caps.StepImage, "image_resource is not a plain registry-image reference; kept in Ext", line)
}

// imageRef reads image_resource as repository[:tag][@digest]. A task file also takes docker-image and a source with
// credential keys, which the task file keeps; an inline config keeps those forms in Ext.
func imageRef(v any, file bool) (string, bool) {
	m, ok := asMap(v)
	if !ok {
		return "", false
	}
	typ, _ := text(m.Vals["type"])
	if typ != "registry-image" && (!file || typ != "docker-image") {
		return "", false
	}
	for _, key := range m.Order {
		if key != "type" && key != "source" {
			return "", false
		}
	}
	src, ok := asMap(m.Vals["source"])
	if !ok {
		return "", false
	}
	if !file {
		for _, key := range src.Order {
			if key != "repository" && key != "tag" && key != "digest" {
				return "", false
			}
		}
	}
	return refOfSource(src, "", nil)
}

// env merges the config params and the step params into the step env; step params win. A value with no Value
// form stays in Ext.
func (jb *jobBuilder) env(st *ir.Step, k *sink, cfgParams *omap, r *reader) {
	sp, _ := asMap(takeVal(r, "params"))
	if sp == nil {
		r.Release("params")
	}
	unread := map[string]any{}
	set := func(m *omap) {
		if m == nil {
			return
		}
		for _, name := range m.Order {
			v, ok := jb.paramValue(m.Vals[name])
			if !ok {
				unread[name] = plain(m.Vals[name])
				continue
			}
			delete(unread, name)
			replaced := false
			for i := range st.Env {
				if st.Env[i].Name == name {
					st.Env[i].Value, replaced = v, true
				}
			}
			if !replaced {
				st.Env = append(st.Env, ir.Var{Name: name, Value: v})
			}
		}
	}
	set(cfgParams)
	set(sp)
	if len(unread) > 0 {
		names := make([]string, 0, len(unread))
		for n := range unread {
			names = append(names, n)
		}
		sort.Strings(names)
		// An unreadable param replaces a readable one of the same name, so the readable one must not stay.
		st.Env = dropVars(st.Env, unread)
		k.Out[ExtParams] = unread
		k.Note(caps.StepEnv, "params "+strings.Join(names, ", ")+" have no Value form (a local var, a var source or text around a var); kept in Ext", 0)
	}
}

func dropVars(env []ir.Var, drop map[string]any) []ir.Var {
	out := env[:0]
	for _, v := range env {
		if _, gone := drop[v.Name]; !gone {
			out = append(out, v)
		}
	}
	return out
}

// paramValue reads one task param. Concourse resolves every ((name)) from the credential manager and masks it,
// so a whole ((name)) is a secret unless a set_pipeline step of the file passes the name in vars; a pipemaster
// marker can still say that it was a var. Text with a var in it, a local var and a var source have no Value form.
func (jb *jobBuilder) paramValue(v any) (ir.Value, bool) {
	t, ok := text(v)
	if !ok {
		return ir.Value{}, false
	}
	if !strings.Contains(t, varOpen) {
		return ir.Value{Lit: t}, true
	}
	if lm := localVar.FindStringSubmatch(t); lm != nil && jb.axes[lm[1]] {
		return ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixMatrix, lm[1]}}}, true
	}
	mm := wholeVar.FindStringSubmatch(t)
	if mm == nil {
		return ir.Value{}, false
	}
	name := mm[1]
	if jb.b.plainVars[name] && identVar.MatchString(name) {
		return ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixVars, name}}}, true
	}
	jb.b.defaulted[name] = true
	return ir.Value{Kind: ir.ValueSecret, Secret: name}, true
}

func isPowershell(cmd string) bool {
	switch strings.ToLower(path.Base(strings.ReplaceAll(cmd, "\\", "/"))) {
	case "pwsh", "powershell", "pwsh.exe", "powershell.exe":
		return true
	}
	return false
}

// workspaceForm is true for the task shape the emitter writes: one workspace input and output, a dir in the
// workspace, and no mapping other than workspace from a git resource. The emitter writes all of it back.
func (jb *jobBuilder) workspaceForm(m, cfg *omap) bool {
	only := func(v any) bool {
		l, ok := v.([]any)
		if !ok || len(l) != 1 {
			return false
		}
		item, ok := asMap(l[0])
		if !ok || len(item.Order) != 1 {
			return false
		}
		n, _ := text(item.Vals["name"])
		return n == workspaceName
	}
	if !only(cfg.Vals["inputs"]) || !only(cfg.Vals["outputs"]) {
		return false
	}
	run, ok := asMap(cfg.Vals["run"])
	if !ok {
		return false
	}
	if dir, _ := text(run.Vals["dir"]); dir != workspaceName && !strings.HasPrefix(dir, workspaceName+"/") {
		return false
	}
	if v, has := m.Vals["input_mapping"]; has {
		im, ok := asMap(v)
		if !ok || len(im.Order) != 1 {
			return false
		}
		src, _ := text(im.Vals[workspaceName])
		rr := jb.b.res[src]
		return rr != nil && rr.typ == "git"
	}
	return true
}
