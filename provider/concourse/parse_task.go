package concourse

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	// ExtTaskDigest is the step Ext key that holds the digest of what the parser read from a task file or a checkout
	// get. The emitter writes the reference again only while the IR step still has that digest.
	ExtTaskDigest = "_task_digest"
	// ExtFileParams is the step Ext key of the params that the task file defaults, in file order.
	ExtFileParams = "_file_params"

	keyImage    = "image"
	keyFile     = "file"
	keyVersion  = "version"
	maxTaskFile = 1 << 20
)

// getInfo is a get step of the plan, by the artifact name it makes.
type getInfo struct {
	res *resource
	m   *omap
}

// taskArg is the marker of a task that points at a task file. It holds what the parser read from the file, so a
// parser that cannot read the file restores the same step.
type taskArg struct {
	File    string      `json:"file"`
	Shell   string      `json:"shell,omitempty"`
	WorkDir string      `json:"workdir,omitempty"`
	Image   string      `json:"image,omitempty"`
	Script  []string    `json:"script"`
	Params  [][2]string `json:"params,omitempty"`
}

// collectGets records every get of the plan by artifact name.
func (jb *jobBuilder) collectGets(plan any) {
	jb.gets = map[string]getInfo{}
	walk(plan, false, func(m *omap) {
		if name, ok := text(m.Vals["get"]); ok && m.Vals["across"] == nil {
			jb.gets[name] = getInfo{res: jb.b.res[resourceName(m)], m: m}
		}
	})
}

// isRepository is true for the git resource that watches the repository of the converted pipeline: its uri is a
// ((var)), or it is the only git uri of the pipeline (the rule that gitTrigger applies to the triggers).
func (b *pipeBuilder) isRepository(r *resource) bool {
	if !b.builtin(r, typeGit) {
		return false
	}
	src := r.source()
	if src == nil {
		return false
	}
	uri, _ := text(src.Vals["uri"])
	return !literalURI(uri) || b.distinctGitURIs() <= 1
}

// taskPath turns file: <artifact>/<path> into a path under the input directory, or says why it cannot.
func (jb *jobBuilder) taskPath(file string) (string, string) {
	if strings.Contains(file, varOpen) {
		return "", "the path holds a ((var))"
	}
	art, rest, ok := strings.Cut(file, "/")
	if !ok || art == "" || rest == "" {
		return "", "the path names no artifact"
	}
	g, ok := jb.gets[art]
	if !ok || g.res == nil {
		return "", "no get of the job makes the artifact " + art
	}
	if !jb.b.isRepository(g.res) {
		return "", "the artifact " + art + " is not the git resource of the converted repository"
	}
	if v, pinned := g.m.Vals[keyVersion]; pinned {
		if s, isText := text(v); !isText || (s != "latest" && s != "every") {
			return "", "the get of " + art + " pins a version, and the working tree holds another"
		}
	}
	if !fs.ValidPath(rest) {
		return "", "the path leaves the input directory"
	}
	return rest, ""
}

// loadTaskFile reads a task file under the input directory. A link on the way, a file over the size limit or text
// that is no mapping gives no config.
func (jb *jobBuilder) loadTaskFile(name string) (*omap, string) {
	fsys := jb.b.fsys
	if fsys == nil {
		return nil, "the parser has no input directory"
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		fi, err := fs.Lstat(fsys, strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, "the file is missing"
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, "the path goes through a link"
		}
		if i == len(parts)-1 && (!fi.Mode().IsRegular() || fi.Size() > maxTaskFile) {
			return nil, "the file is no regular file or is over the size limit"
		}
	}
	top, _, err := readFile(fsys, name, jb.b.rep)
	if err != nil {
		return nil, "the file cannot be read: " + err.Error()
	}
	if top == nil {
		return nil, "the file is empty"
	}
	return top, ""
}

// fileDefaults keeps the params of a task file that have a text value; a null or empty value only declares the
// param, which the step must set.
func fileDefaults(params *omap) *omap {
	out := omapNew()
	if params == nil {
		return out
	}
	for _, name := range params.Order {
		if t, ok := text(params.Vals[name]); ok && t != "" {
			out.Set(name, params.Vals[name], params.Lines[name])
		}
	}
	return out
}

// fileTask reads the task file of a step into the step. It returns the file params and whether the step is now a
// script step. A file it cannot use leaves the step a task that holds the path, and the report names the path.
func (jb *jobBuilder) fileTask(st *ir.Step, k *sink, m *omap, file, written string, line int) (*omap, bool) {
	rel, why := jb.taskPath(file)
	if why == "" {
		var cfg *omap
		if cfg, why = jb.loadTaskFile(rel); cfg != nil {
			ts := &ir.Step{}
			scratchRep := &report.Report{}
			scratch := newSink(scratchRep, rel, jb.path+"/step/"+st.ID)
			jb.inFile = true
			params := fileDefaults(jb.config(ts, scratch, cfg, 0, false))
			jb.inFile = false
			if len(ts.Script) > 0 {
				jb.fileShell(ts, scratch)
				ts.Script = jb.fileScript(ts, m, file, cfg, scratch, line)
				st.Kind, st.Script, st.Shell, st.WorkDir, st.Image = ir.StepScript, ts.Script, ts.Shell, ts.WorkDir, ts.Image
				jb.fileNotes(file, scratch, scratchRep, m, line)
				return params, true
			}
			why = "the task file has no script that the IR can hold"
		}
	}
	var a taskArg
	if jb.b.arg(jb.path+"/step/"+written, caps.StepScript, &a) && a.File == file && len(a.Script) > 0 {
		st.Kind, st.Script, st.Shell, st.WorkDir = ir.StepScript, a.Script, a.Shell, a.WorkDir
		if a.Image != "" {
			st.Image = &ir.Image{Ref: a.Image}
		}
		params := omapNew()
		for _, kv := range a.Params {
			params.Set(kv[0], ynode.Scal{N: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: kv[1]}}, 0)
		}
		return params, true
	}
	st.Kind = ir.StepTask
	k.Note(caps.StepScript, "task file "+file+" is referenced, not read ("+why+"); the step keeps the path in Ext", line)
	return nil, false
}

// fileShell drops the shell of a task that runs sh with only the flags -c, -e and -x: fileScript puts the -e and -x
// options into the script, so the target needs no shell.
func (jb *jobBuilder) fileShell(ts *ir.Step, scratch *sink) {
	if ts.Shell != shellCommand {
		return
	}
	flags, _ := scratch.Out[ExtShellFlags].([]any)
	for _, f := range flags {
		if t, _ := f.(string); strings.Trim(t, "-cex") != "" {
			return
		}
	}
	ts.Shell = ""
}

// shellOptions returns the set lines of the shell flags that the task ran with: -e stops at the first failing
// command and -x traces. A task that runs a program directly, or a PowerShell task, has none.
func shellOptions(ts *ir.Step, scratch *sink) []string {
	if _, direct := scratch.Out[ExtCommand]; direct || isPowershell(ts.Shell) {
		return nil
	}
	flags := defaultFlags
	if l, ok := scratch.Out[ExtShellFlags].([]any); ok {
		flags = ""
		for _, f := range l {
			t, _ := f.(string)
			flags += t
		}
	}
	var out []string
	if strings.Contains(flags, "e") {
		out = append(out, "set -e")
	}
	if strings.Contains(flags, "x") {
		out = append(out, "set -x")
	}
	return out
}

// fileScript builds the script of a task file: the shell options, the lines that lay out the inputs and outputs, a
// cd to run.dir, and then the script of the task. The cd sits in the script, after the layout, because a working
// directory field of a target applies before the first script line and the directory can be an input.
func (jb *jobBuilder) fileScript(ts *ir.Step, m *omap, file string, cfg *omap, scratch *sink, line int) []string {
	opts := shellOptions(ts, scratch)
	layout := jb.fileLayout(ts, m, file, cfg, scratch, line)
	var enter []string
	if ts.WorkDir != "" && len(layout) > 0 {
		enter = append(enter, "cd "+shellQuote(ts.WorkDir))
		ts.WorkDir = ""
	}
	if len(opts)+len(enter) == 0 {
		return append(layout, ts.Script...)
	}
	// The subshell keeps set and cd from the steps that follow in a target that runs all lines in one shell.
	body := append(append(append([]string{"("}, opts...), layout...), enter...)
	return append(append(body, ts.Script...), ")")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// artifactDir matches a directory name or relative path that is safe in a script line.
var artifactDir = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

// ioEntry is one input or output of a task file: its name, its directory and whether the input is optional.
type ioEntry struct {
	name, dir string
	optional  bool
}

func ioEntries(v any) []ioEntry {
	l, _ := v.([]any)
	var out []ioEntry
	for _, e := range l {
		m, ok := asMap(e)
		if !ok {
			continue
		}
		name, ok := text(m.Vals["name"])
		if !ok {
			continue
		}
		en := ioEntry{name: name, dir: name}
		if p, ok := text(m.Vals["path"]); ok && p != "" {
			en.dir = p
		}
		en.optional, _ = boolOf(m.Vals["optional"])
		out = append(out, en)
	}
	return out
}

// mapping reads input_mapping or output_mapping of the step: task name to artifact name.
func mapping(m *omap, key string) map[string]string {
	out := map[string]string{}
	if mm, ok := asMap(m.Vals[key]); ok {
		for _, n := range mm.Order {
			if t, ok := text(mm.Vals[n]); ok {
				out[n] = t
			}
		}
	}
	return out
}

func appendParent(lines []string, dir string) []string {
	if i := strings.LastIndex(dir, "/"); i > 0 {
		return append(lines, "mkdir -p "+dir[:i])
	}
	return lines
}

// fileLayout returns the script lines that lay out the inputs and outputs of a task file where the script reads
// them, and reports exactly what the lines make. The repository input links to the checkout, an output is a
// directory at its path, and an input that an earlier task made links to that directory when the paths differ.
// input_mapping and output_mapping rename the artifacts, and an optional input with no source is skipped.
func (jb *jobBuilder) fileLayout(ts *ir.Step, m *omap, file string, cfg *omap, scratch *sink, line int) []string {
	if isPowershell(ts.Shell) {
		return nil
	}
	inMap, outMap := mapping(m, "input_mapping"), mapping(m, "output_mapping")
	var lines, made, lost []string
	for _, in := range ioEntries(cfg.Vals["inputs"]) {
		art := in.name
		if mapped, ok := inMap[in.name]; ok {
			art = mapped
		}
		if !artifactDir.MatchString(in.dir) {
			lost = append(lost, "input "+in.name+" (the path is no plain directory name)")
			continue
		}
		if g, ok := jb.gets[art]; ok && jb.b.isRepository(g.res) {
			lines = appendParent(lines, in.dir)
			lines = append(lines, fmt.Sprintf("[ -e %[1]s ] || ln -s \"$PWD\" %[1]s", in.dir))
			made = append(made, fmt.Sprintf("link %s to the checkout (artifact %s)", in.dir, art))
			continue
		}
		if src, ok := jb.outputs[art]; ok {
			if src != in.dir {
				lines = appendParent(lines, in.dir)
				lines = append(lines, fmt.Sprintf("[ -e %[1]s ] || ln -s \"$PWD/%[2]s\" %[1]s", in.dir, src))
				made = append(made, fmt.Sprintf("link %s to %s (artifact %s)", in.dir, src, art))
			}
			continue
		}
		if in.optional {
			made = append(made, "skip the optional input "+in.name+" (artifact "+art+" has no source)")
			continue
		}
		lost = append(lost, "input "+in.name+" (artifact "+art+" comes from a resource or a step that the target does not run)")
	}
	for _, out := range ioEntries(cfg.Vals["outputs"]) {
		art := out.name
		if mapped, ok := outMap[out.name]; ok {
			art = mapped
		}
		if !artifactDir.MatchString(out.dir) {
			lost = append(lost, "output "+out.name+" (the path is no plain directory name)")
			continue
		}
		lines = append(lines, "mkdir -p "+out.dir)
		made = append(made, "make the directory "+out.dir+" (output "+out.name+", artifact "+art+")")
		jb.outputs[art] = out.dir
	}
	if len(made) > 0 {
		jb.k.Rep.Add(report.Entry{
			IRPath: scratch.Path, Feature: caps.StepWorkdir, Level: caps.Emulated, Strategy: "directories made by script lines",
			Detail: "task file " + file + ": the first script lines " + strings.Join(made, "; ") +
				". A link to the checkout is an untracked entry in the working tree, which tools that walk the tree also see",
			SourceLoc: jb.k.Loc(line),
		})
	}
	if len(lost) > 0 {
		jb.k.Rep.Add(report.Entry{
			IRPath: scratch.Path, Feature: caps.StepWorkdir, Level: caps.Lost, Strategy: "kept in the task file",
			Detail:    "task file " + file + ": " + strings.Join(lost, "; ") + "; no script line makes them, and the script reads them as directories of the working directory",
			SourceLoc: jb.k.Loc(line),
		})
	}
	return lines
}

// fileNotes reports what a task file holds that the IR step does not: the keys with no IR field, run keys such as
// user with their values, and the notes of the config reader.
func (jb *jobBuilder) fileNotes(file string, scratch *sink, rep *report.Report, m *omap, line int) {
	add := func(f caps.Feature, detail string) {
		jb.k.Rep.Add(report.Entry{
			IRPath: scratch.Path, Feature: f, Level: caps.Lost, Strategy: "kept in the task file",
			Detail: detail, SourceLoc: jb.k.Loc(line),
		})
	}
	if rest, ok := scratch.Out[ExtConfig].(map[string]any); ok && len(rest) > 0 {
		add(caps.ExtPassthrough, fmt.Sprintf("task file %s: keys %s have no IR field; they stay in the task file", file, strings.Join(sortedKeys(rest), ", ")))
	}
	if run, ok := scratch.Out[ExtRun].(map[string]any); ok && len(run) > 0 {
		var kv []string
		for _, key := range sortedKeys(run) {
			kv = append(kv, fmt.Sprintf("%s=%v", key, run[key]))
		}
		add(caps.ExtPassthrough, "task file "+file+": run keys "+strings.Join(kv, ", ")+" have no IR field and no target runs the task with them; the task file keeps them")
	}
	if _, ok := scratch.Out[ExtImageResource]; ok {
		if name, isText := text(m.Vals[keyImage]); !isText || jb.gets[name].res == nil {
			add(caps.StepImage, "task file "+file+": image_resource is no plain registry-image reference; it stays in the task file")
		}
	}
	for _, en := range rep.Entries {
		if strings.HasPrefix(en.Detail, "task config keys") || strings.HasPrefix(en.Detail, "run keys") || strings.HasPrefix(en.Detail, "image_resource") || strings.HasPrefix(en.Detail, "run.path and run.args") || strings.HasPrefix(en.Detail, "shell flags") {
			continue
		}
		add(en.Feature, "task file "+file+": "+en.Detail)
	}
}

// imageOfGet is the image reference of the registry-image get that the artifact name stands for: repository, tag
// and digest exactly as the resource and the get give them.
func (jb *jobBuilder) imageOfGet(name string) (string, bool) {
	g, ok := jb.gets[name]
	if !ok || g.res == nil {
		return "", false
	}
	if !jb.b.builtin(g.res, "registry-image") && !jb.b.builtin(g.res, "docker-image") {
		return "", false
	}
	src := g.res.source()
	if src == nil {
		return "", false
	}
	var digest string
	if v, pinned := g.m.Vals[keyVersion]; pinned {
		if vm, isMap := asMap(v); isMap {
			if len(vm.Order) != 1 || vm.Order[0] != "digest" {
				return "", false
			}
			digest, _ = text(vm.Vals["digest"])
		} else if s, isText := text(v); !isText || (s != "latest" && s != "every") {
			return "", false
		}
	}
	var creds []string
	ref, ok := refOfSource(src, digest, func(key string) { creds = append(creds, key) })
	if ok && len(creds) > 0 && !jb.credNoted[g.res.name] {
		jb.credNoted[g.res.name] = true
		jb.k.Rep.Add(report.Entry{
			IRPath: jb.path, Feature: caps.StepImage, Level: caps.External, Strategy: "registry credentials",
			Detail:    "the registry-image resource " + g.res.name + " sets " + strings.Join(creds, ", ") + "; the target must hold the registry access that the image needs",
			SourceLoc: jb.k.Loc(g.res.line),
		})
	}
	return ref, ok
}

// refOfSource builds repository[:tag][@digest] from a registry-image source. A key that changes which image is
// pulled (variant, semver, created_at, a var in a value) gives no reference. Credential keys call onCred.
func refOfSource(src *omap, digest string, onCred func(string)) (string, bool) {
	repo, ok := text(src.Vals["repository"])
	if !ok || repo == "" || strings.Contains(repo, varOpen) {
		return "", false
	}
	ref, tag := repo, ""
	for _, key := range src.Order {
		switch key {
		case "repository":
		case "tag", "digest":
			t, ok := text(src.Vals[key])
			if !ok || t == "" || strings.Contains(t, varOpen) {
				return "", false
			}
			if key == "tag" {
				tag = t
			} else if digest == "" {
				digest = t
			}
		case "username", "password", "aws_access_key_id", "aws_secret_access_key", "aws_session_token", "aws_region", "ca_certs":
			if onCred != nil {
				onCred(key)
			}
		default:
			return "", false
		}
	}
	if tag != "" && digest == "" {
		ref += ":" + tag
	}
	if digest != "" {
		ref += "@" + digest
	}
	return ref, true
}

// stepImage reads the image: key of a task, which names a get of a registry-image resource and wins over the
// image_resource of the config.
func (jb *jobBuilder) stepImage(st *ir.Step, k *sink, r *reader) {
	v, ok := r.Take(keyImage)
	if !ok {
		return
	}
	name, isText := text(v)
	if !isText {
		r.Release(keyImage)
		return
	}
	ref, ok := jb.imageOfGet(name)
	if !ok {
		r.Release(keyImage)
		return
	}
	st.Image = &ir.Image{Ref: ref}
	k.Out[keyImage] = name
	jb.fresh[st] = true
}

// taskDigest is the digest of the step fields that a task file or a checkout get gives. image is the image that the
// step runs in, from the step or from the job.
func taskDigest(st *ir.Step, image string) string {
	if st.Kind == ir.StepAction {
		with := map[string]string{}
		for key, v := range st.With {
			with[key] = v.Lit
		}
		return digestOf(map[string]any{"action": actionRef(st.Action), "with": with})
	}
	return digestOf(map[string]any{"script": st.Script, "shell": st.Shell, "workdir": st.WorkDir, "image": image})
}

// hoistImage moves the image of the task steps to the job when every script step of the job runs in the same image
// that a task file or an image get gave. GitLab and the other providers set the image per job, so the step image
// would be lost there.
func (jb *jobBuilder) hoistImage() {
	if jb.job.Container != nil {
		return
	}
	ref := ""
	for _, st := range jb.job.Steps {
		if st.Kind != ir.StepScript {
			continue
		}
		if st.Image == nil || !jb.fresh[st] || (ref != "" && st.Image.Ref != ref) {
			return
		}
		ref = st.Image.Ref
	}
	if ref == "" {
		return
	}
	jb.job.Container = &ir.Image{Ref: ref}
	for _, st := range jb.job.Steps {
		if st.Kind == ir.StepScript {
			st.Image = nil
		}
	}
}

// imageNames lists the artifact names that the tasks of the plan use as their image: key.
func imageNames(plan any) map[string]bool {
	out := map[string]bool{}
	walk(plan, true, func(m *omap) {
		if m.Vals["task"] == nil {
			return
		}
		if n, ok := text(m.Vals[keyImage]); ok {
			out[n] = true
		}
	})
	return out
}

// checkoutStep reads an in_parallel step that holds only gets as the checkout of the repository. The gets of an
// image that a task of the job runs in are read through the task, so they need no step of their own. Any other get
// leaves the step as it is.
func (jb *jobBuilder) checkoutStep(m *omap, gets []*omap, used map[string]bool) *ir.Step {
	ref, hasAction := actions.Reverse("checkout")
	if !hasAction {
		return nil
	}
	var repoGet *omap
	for _, g := range gets {
		name, ok := text(g.Vals["get"])
		if !ok || g.Vals["across"] != nil {
			return nil
		}
		info := jb.gets[name]
		switch {
		case jb.b.isRepository(info.res) && repoGet == nil:
			repoGet = g
		case used[name]:
			if _, ok := jb.imageOfGet(name); !ok {
				return nil
			}
		default:
			return nil
		}
	}
	if repoGet == nil {
		return nil
	}
	st := jb.named("checkout")
	st.Kind, st.Action = ir.StepAction, &ref
	if p, ok := asMap(repoGet.Vals["params"]); ok {
		if d, ok := text(p.Vals["depth"]); ok {
			if n, err := strconv.Atoi(d); err == nil && n > 0 {
				st.With = map[string]ir.Value{"fetch-depth": {Lit: d}}
			}
		}
	}
	k := newSink(jb.k.Rep, jb.b.file, jb.path+"/step/"+st.ID)
	k.Out[ExtKind] = "in_parallel"
	k.Out[ExtStep] = plain(m)
	k.Out[ExtTaskDigest] = taskDigest(st, "")
	st.Ext = k.Ext()
	jb.k.Rep.Add(report.Entry{
		IRPath: jb.path + "/step/" + st.ID, Feature: caps.StepAction, Level: caps.Emulated, Strategy: "checkout step",
		Detail: "the get of the repository resource is the checkout step; the other gets of the step give the image of the tasks", SourceLoc: jb.k.Loc(m.Lines["in_parallel"]),
	})
	return st
}

// acrossMatrix reads a plan that is one across step over literal values as the job matrix, one axis per var. The
// body of the step becomes the steps of the job. An across with a ((var)) in its values, a key the matrix cannot
// hold, or other steps in the plan stays one step that the IR cannot run.
func (jb *jobBuilder) acrossMatrix(steps []any) []any {
	if len(steps) != 1 {
		return steps
	}
	step, ok := asMap(steps[0])
	if !ok {
		return steps
	}
	items, ok := step.Vals["across"].([]any)
	if !ok || len(items) == 0 {
		return steps
	}
	mx := &ir.Matrix{}
	inFlight := 0
	for _, raw := range items {
		a, ok := asMap(raw)
		if !ok {
			return steps
		}
		name, values, ok := acrossAxis(a)
		if !ok {
			return steps
		}
		if v, set := a.Vals[maxInFlightK]; set {
			if len(items) > 1 {
				return steps
			}
			n, ok := acrossInFlight(v)
			if !ok {
				return steps
			}
			inFlight = n
		} else if len(items) == 1 {
			inFlight = 1
		}
		mx.Axes = append(mx.Axes, ir.Axis{Name: name, Values: values})
	}
	mx.MaxParallel = inFlight
	body, ok := acrossBody(step, mx)
	if !ok {
		return steps
	}
	jb.job.Matrix = mx
	jb.axes = map[string]bool{}
	for _, a := range mx.Axes {
		jb.axes[a.Name] = true
	}
	jb.emulated(caps.JobMatrix, "across over the axes", "the across step over literal values is the job matrix with one axis per var", step.Lines["across"])
	return body
}

// acrossAxis reads one across item: a var name and a list of literal values.
func acrossAxis(a *omap) (string, []string, bool) {
	for _, key := range a.Order {
		if key != "var" && key != "values" && key != maxInFlightK {
			return "", nil, false
		}
	}
	name, ok := text(a.Vals["var"])
	if !ok || !identVar.MatchString(name) {
		return "", nil, false
	}
	list, ok := a.Vals["values"].([]any)
	if !ok || len(list) == 0 {
		return "", nil, false
	}
	var values []string
	for _, v := range list {
		t, ok := text(v)
		if !ok || strings.Contains(t, varOpen) {
			return "", nil, false
		}
		values = append(values, t)
	}
	return name, values, true
}

// acrossInFlight reads max_in_flight: all is no limit, a number is the limit.
func acrossInFlight(v any) (int, bool) {
	t, ok := text(v)
	if !ok {
		return 0, false
	}
	if t == acrossAll {
		return 0, true
	}
	return positiveInt(v)
}

// acrossBody returns the steps that the across wraps and applies fail_fast to the matrix. A do step with only
// across and fail_fast beside it gives its list; a step of any other kind gives itself without the across.
func acrossBody(step *omap, mx *ir.Matrix) ([]any, bool) {
	rest := omapNew()
	for _, key := range step.Order {
		switch key {
		case "across":
		case "fail_fast":
			on, ok := boolOf(step.Vals[key])
			if !ok {
				return nil, false
			}
			if on {
				t := true
				mx.FailFast = &t
			}
		default:
			rest.Set(key, step.Vals[key], step.Lines[key])
		}
	}
	if list, ok := rest.Vals[keyDo].([]any); ok && len(rest.Order) == 1 {
		return list, true
	}
	if rest.Vals[keyDo] != nil {
		return nil, false
	}
	return []any{rest}, true
}
