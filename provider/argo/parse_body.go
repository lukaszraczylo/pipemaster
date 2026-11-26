package argo

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	// mainStep is the ID of the step of a template with one container; Argo reads artifacts and outputs of the container named main.
	mainStep     = "main"
	checkoutStep = "checkout"
	keyInputs    = "inputs"
	keyOutputs   = "outputs"
	nodeOS       = "kubernetes.io/os"
	nodeArch     = "kubernetes.io/arch"
	keyDeps      = "dependencies"
)

var githubRepo = regexp.MustCompile(`^https://github\.com/([^/]+)/([^/]+?)(?:\.git)?/?$`)

// body reads the template that a task runs into the job.
func (b *jobBuilder) body(t *omap) error {
	r := newReader(t)
	r.Take("name")
	kind, _ := templateKind(t)
	b.planVolumes(t)
	if err := b.inputs(r); err != nil {
		return err
	}
	b.outputs(r)
	b.retry(r)
	b.timeouts(r)
	if v, ok := r.Take("sidecars"); ok {
		if err := b.sidecars(v); err != nil {
			return err
		}
	}
	if v, ok := r.Take("volumes"); ok && !isWorkspaceVolumes(v) {
		b.keepVolumes(withoutWorkspace(v), r.Line("volumes"))
	}
	if v, ok := r.Take("nodeSelector"); ok {
		b.nodeSelector(v, r.Line("nodeSelector"))
	}
	var err error
	switch kind {
	case kindContainer, kindScript:
		err = b.container(r, kind)
	case kindContainerSet:
		err = b.containerSet(r)
	default:
		b.taskStep(r, kind)
	}
	if err != nil {
		return err
	}
	b.k.KeepRest(r)
	b.reportVolumes()
	b.keepMountPaths()
	b.addVolumeDirs()
	b.uniqueStepIDs()
	b.collapseSteps()
	b.collapseActions()
	b.jobEnvMarker()
	return nil
}

// inputs reads the template inputs: parameter defaults resolve text, a git artifact is a checkout step, and
// anything else stays in Ext.
func (b *jobBuilder) inputs(r *reader) error {
	v, ok := r.Take(keyInputs)
	if !ok {
		return nil
	}
	m, isMap := asMap(v)
	if !isMap {
		b.k.Keep(keyInputs, v, caps.ExtPassthrough, "inputs is not a mapping; kept in Ext", r.Line(keyInputs))
		return nil
	}
	inr := newReader(m)
	kept := map[string]any{}
	params, _ := inr.Take("parameters")
	var keptParams []any
	for _, e := range listOf(params) {
		pm, ok := asMap(e)
		name, _ := text(valueOf(pm, "name"))
		if !ok || name == "" {
			return fmt.Errorf("an input parameter needs a name")
		}
		pr := newReader(pm)
		pr.Take("name")
		for _, key := range []string{"value", "default"} {
			if d, ok := textOf(pr.Take(key)); ok {
				if _, set := b.tx.defaults[name]; !set {
					b.tx.defaults[name] = d
				}
			} else {
				pr.Release(key)
			}
		}
		if rest := pr.Rest(); len(rest) > 0 {
			keptParams = append(keptParams, plain(e))
			b.k.Note(caps.ExtPassthrough, "input param "+name+": key "+strings.Join(rest, ", ")+" has no IR field; kept in Ext", pm.Lines["name"])
		}
	}
	if len(keptParams) > 0 {
		kept["parameters"] = keptParams
	}
	arts, _ := inr.Take("artifacts")
	var keptArts []any
	for _, e := range listOf(arts) {
		if !b.gitArtifact(e) {
			keptArts = append(keptArts, plain(e))
			b.k.Note(caps.JobNeedsArtifacts, "an input artifact has no IR field beside the needs flag; kept in Ext", 0)
		}
	}
	if len(keptArts) > 0 {
		kept["artifacts"] = keptArts
	}
	for _, key := range inr.Rest() {
		kept[key] = plain(m.Vals[key])
		b.k.Note(caps.ExtPassthrough, "inputs."+key+" has no IR field; kept in Ext", m.Lines[key])
	}
	if len(kept) > 0 {
		b.k.Out[keyInputs] = kept
	}
	return nil
}

// gitArtifact turns a git input artifact into a checkout step. A repository that is not on GitHub, or a key that
// the checkout action has no input for, makes a task step that keeps the artifact in Ext. It reports false for an
// artifact that is no git artifact.
func (b *jobBuilder) gitArtifact(e any) bool {
	am, ok := asMap(e)
	gm, isGit := asMap(valueOf(am, "git"))
	if !ok || !isGit {
		return false
	}
	st := &ir.Step{ID: checkoutStep}
	if b.gitArts == nil {
		b.gitArts = map[*ir.Step]string{}
	}
	b.gitArts[st] = canonJSON(plain(e))
	k := newSink(b.c.p.rep, b.c.file, b.path+"/step/"+st.ID)
	repoURL, _ := text(gm.Vals["repo"])
	m := githubRepo.FindStringSubmatch(repoURL)
	ctxRepo := repoURL == cloneHost+"{{workflow.parameters.ctx-repo}}.git"
	whole := (m != nil || ctxRepo) && len(am.Order) <= 3
	for _, key := range am.Order {
		whole = whole && (key == "name" || key == "path" || key == "git")
	}
	for _, key := range gm.Order {
		whole = whole && (key == "repo" || key == "revision" || key == "depth")
	}
	ref, hasAction := actions.Reverse("checkout")
	if !whole || !hasAction {
		st.Kind = ir.StepTask
		k.Keep("artifact", e, caps.StepAction, "git artifact "+repoURL+" has no checkout step form; kept in Ext", am.Lines["name"])
		st.Ext = k.Ext()
		b.job.Steps = append(b.job.Steps, st)
		return true
	}
	st.Kind = ir.StepAction
	st.Action = &ref
	st.With = map[string]ir.Value{}
	if !ctxRepo {
		st.With["repository"] = ir.Value{Lit: m[1] + "/" + m[2]}
	}
	for _, kv := range [][2]string{{"revision", "ref"}, {"depth", "fetch-depth"}} {
		if t, ok := text(gm.Vals[kv[0]]); ok && (!ctxRepo || t != "{{workflow.parameters.ctx-sha}}") {
			st.With[kv[1]] = ir.Value{Lit: t}
		}
	}
	if t, ok := text(am.Vals["path"]); ok && t != workspaceMount {
		st.With["path"] = ir.Value{Lit: strings.TrimPrefix(t, workspaceMount+"/")}
	}
	if name, ok := text(am.Vals["name"]); ok && name != gitArtifactName {
		k.Keep("artifact", map[string]any{"name": name}, caps.StepAction, "the name of git artifact "+name+" has no IR field; kept in Ext", am.Lines["name"])
		st.Ext = k.Ext()
	}
	b.job.Steps = append(b.job.Steps, st)
	return true
}

// outputs maps output parameters to job outputs and output artifacts to job artifacts; their other keys stay in Ext.
func (b *jobBuilder) outputs(r *reader) {
	v, ok := r.Take(keyOutputs)
	if !ok {
		return
	}
	m, isMap := asMap(v)
	if !isMap {
		b.k.Keep(keyOutputs, v, caps.JobOutputs, "outputs is not a mapping; kept in Ext", r.Line(keyOutputs))
		return
	}
	or := newReader(m)
	kept := map[string]any{}
	params, _ := or.Take("parameters")
	keptParams := map[string]any{}
	for _, e := range listOf(params) {
		pm, ok := asMap(e)
		name, _ := text(valueOf(pm, "name"))
		if !ok || name == "" {
			keptParams["#"+strconv.Itoa(len(keptParams))] = plain(e)
			continue
		}
		b.job.Outputs = append(b.job.Outputs, ir.Output{Name: name})
		rest := restKeys(pm, "name")
		if len(rest) == 0 || isOutputFile(pm, rest, name) {
			b.fileOutputs = append(b.fileOutputs, name)
		}
		if len(rest) > 0 && !isOutputFile(pm, rest, name) {
			keptParams[name] = plain(pm.Subset(rest))
			b.k.Note(caps.JobOutputs, "outputs.parameters "+name+": key "+strings.Join(rest, ", ")+" has no IR field; kept in Ext", pm.Lines["name"])
		}
	}
	if len(keptParams) > 0 {
		kept["parameters"] = keptParams
	}
	arts, _ := or.Take("artifacts")
	keptArts := map[string]any{}
	for _, e := range listOf(arts) {
		am, ok := asMap(e)
		name, _ := text(valueOf(am, "name"))
		if !ok || name == "" {
			keptArts["#"+strconv.Itoa(len(keptArts))] = plain(e)
			continue
		}
		a := ir.Artifact{Name: name}
		if p, ok := text(am.Vals["path"]); ok {
			a.Paths = []string{p}
		}
		b.job.Artifacts = append(b.job.Artifacts, a)
		if b.outArts == nil {
			b.outArts = map[string]string{}
		}
		b.outArts[name] = canonJSON(plain(e))
		if rest := restKeys(am, "name", "path"); len(rest) > 0 {
			keptArts[name] = plain(am.Subset(rest))
			b.k.Note(caps.JobArtifacts, "outputs.artifacts "+name+": key "+strings.Join(rest, ", ")+" has no IR field; kept in Ext", am.Lines["name"])
		}
	}
	if len(keptArts) > 0 {
		kept["artifacts"] = keptArts
	}
	for _, key := range or.Rest() {
		kept[key] = plain(m.Vals[key])
		b.k.Note(caps.JobOutputs, "outputs."+key+" has no IR field; kept in Ext", m.Lines[key])
	}
	if len(kept) > 0 {
		b.k.Out[keyOutputs] = kept
	}
}

// isOutputFile is true for the one valueFrom that the emitter writes for an output: the path of its file and an
// empty default. The emitter writes it again, so the Ext does not keep it.
func isOutputFile(pm *omap, rest []string, name string) bool {
	vf, ok := asMap(valueOf(pm, "valueFrom"))
	if !ok || len(rest) != 1 || rest[0] != "valueFrom" || len(vf.Order) != 2 {
		return false
	}
	p, hasPath := text(vf.Vals["path"])
	d, hasDefault := text(vf.Vals["default"])
	return hasPath && hasDefault && p == outputFile(name) && d == ""
}

// retry maps retryStrategy.limit to attempts, so an explicit limit of 0 is one attempt and an absent strategy is none;
// the other keys stay in Ext.
func (b *jobBuilder) retry(r *reader) {
	v, ok := r.Take("retryStrategy")
	if !ok {
		return
	}
	m, isMap := asMap(v)
	limit, hasLimit := textOf(valueOf(m, "limit"), isMap)
	n, err := strconv.Atoi(limit)
	if !isMap || !hasLimit || err != nil || n < 0 {
		b.k.Keep("retryStrategy", v, caps.JobRetry, "retryStrategy has no limit that is a count; kept in Ext", r.Line("retryStrategy"))
		return
	}
	b.job.Retry = &ir.Retry{Attempts: n + 1}
	if rest := restKeys(m, "limit"); len(rest) > 0 {
		b.k.Keep("retryStrategy", m.Subset(rest), caps.JobRetry, "retryStrategy keys beside limit (retryPolicy, backoff, expression) have no IR field; kept in Ext", r.Line("retryStrategy"))
	}
}

// timeouts maps timeout, or activeDeadlineSeconds, to the job timeout.
func (b *jobBuilder) timeouts(r *reader) {
	if v, ok := r.Take("timeout"); ok {
		if d, ok := parseDuration(v); ok {
			b.job.Timeout = &d
		} else {
			b.k.Keep("timeout", v, caps.JobTimeout, "timeout has no positive duration; kept in Ext", r.Line("timeout"))
		}
	}
	if v, ok := r.Take("activeDeadlineSeconds"); ok {
		secs, err := strconv.Atoi(strings.TrimSpace(textString(v)))
		if err != nil || secs <= 0 || b.job.Timeout != nil {
			b.k.Keep("activeDeadlineSeconds", v, caps.JobTimeout, "activeDeadlineSeconds is not a count of seconds, or timeout already set the job timeout; kept in Ext", r.Line("activeDeadlineSeconds"))
			return
		}
		d := ir.Duration(time.Duration(secs) * time.Second)
		b.job.Timeout = &d
	}
}

func textString(v any) string {
	t, _ := text(v)
	return t
}

// nodeSelector maps the os and arch labels to a runner platform label; any other selector stays in Ext.
func (b *jobBuilder) nodeSelector(v any, line int) {
	m, ok := asMap(v)
	osName, hasOS := textOf(valueOf(m, nodeOS), ok)
	arch, hasArch := textOf(valueOf(m, nodeArch), ok)
	if !ok || !hasOS || !hasArch || len(m.Order) != 2 {
		b.k.Keep("nodeSelector", v, caps.JobRunner, "nodeSelector is not only the os and arch labels; kept in Ext", line)
		return
	}
	b.job.Runner.Labels = append(b.job.Runner.Labels, osName+"/"+arch)
}

// sidecars maps each sidecar to a service; its other keys stay in Ext.
func (b *jobBuilder) sidecars(v any) error {
	extra := ynode.NewMap()
	for _, e := range listOf(v) {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return fmt.Errorf("a sidecar needs a name")
		}
		r := newReader(m)
		r.Take("name")
		sv := ir.Service{Name: name}
		if t, ok := textOf(r.Take("image")); ok {
			ref, err := b.tx.template(t)
			if err != nil {
				return err
			}
			sv.Image.Ref = ref
		}
		if ev, ok := r.Take("env"); ok {
			vars, from, err := b.envList(ev)
			if err != nil {
				return err
			}
			sv.Env = vars
			if len(from.Order) > 0 {
				r.Release("env")
			}
		}
		if pv, ok := r.Take("ports"); ok {
			ports, whole := ynode.ContainerPorts(pv)
			sv.Ports = ports
			if !whole {
				r.Release("ports")
				sv.Ports = nil
			}
		}
		b.serviceMarker(&sv)
		b.job.Services = append(b.job.Services, sv)
		if rest := r.Rest(); len(rest) > 0 {
			extra.Set(name, m.Subset(rest), m.Lines["name"])
			b.k.Note(caps.JobServices, "sidecar "+name+": key "+rest[0]+" has no IR field; kept in Ext", m.Lines["name"])
		}
	}
	if len(extra.Order) > 0 {
		b.k.Out["sidecars"] = plain(extra)
	}
	return nil
}

// envList reads a Kubernetes env list; entries with valueFrom stay out of the list and are returned by name.
func (b *jobBuilder) envList(v any) ([]ir.Var, *omap, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("env must be a list")
	}
	var vars []ir.Var
	from := ynode.NewMap()
	for _, e := range list {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if !ok || name == "" {
			return nil, nil, fmt.Errorf("an env entry needs a name")
		}
		if vf, has := m.Vals["valueFrom"]; has {
			if v, ok := storeValue(vf); ok && len(m.Order) == 2 {
				vars = append(vars, ir.Var{Name: name, Value: v})
				continue
			}
			from.Set(name, vf, 0)
			continue
		}
		raw, _ := text(m.Vals["value"])
		vars = append(vars, ir.Var{Name: name, Value: b.tx.value(raw)})
	}
	return vars, from, nil
}

// storeValue reads a reference to the secret or variable store that the emitter writes as a secret or a vars value.
func storeValue(vf any) (ir.Value, bool) {
	m, ok := asMap(vf)
	if !ok || len(m.Order) != 1 {
		return ir.Value{}, false
	}
	kind := m.Order[0]
	ref, ok := asMap(m.Vals[kind])
	if !ok || len(ref.Order) != 2 {
		return ir.Value{}, false
	}
	store, _ := text(ref.Vals["name"])
	key, hasKey := text(ref.Vals["key"])
	switch {
	case !hasKey || key == "":
		return ir.Value{}, false
	case kind == kindSecretRef && store == secretStore:
		return ir.Value{Kind: ir.ValueSecret, Secret: key}, true
	case kind == kindConfigRef && store == configStore:
		return ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixVars, key}}}, true
	}
	return ir.Value{}, false
}

// container reads a container or script template into one step.
func (b *jobBuilder) container(r *reader, kind string) error {
	v, _ := r.Take(kind)
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("%s must be a mapping", kind)
	}
	st, image, err := b.stepOf(mainStep, newReader(m), kind == kindScript)
	if err != nil {
		return err
	}
	b.job.Container = imageOf(image)
	b.job.Steps = append(b.job.Steps, st)
	return nil
}

func imageOf(ref string) *ir.Image {
	if ref == "" || ref == defaultImage {
		return nil
	}
	return &ir.Image{Ref: ref}
}

// stepOf reads a container body into a step and returns its image. The caller has taken the keys that it reads.
func (b *jobBuilder) stepOf(id string, r *reader, script bool) (*ir.Step, string, error) {
	st := &ir.Step{ID: id, Kind: ir.StepScript}
	k := newSink(b.c.p.rep, b.c.file, b.path+"/step/"+id)
	var image string
	if t, ok := textOf(r.Take("image")); ok {
		var err error
		if image, err = b.tx.template(t); err != nil {
			return nil, "", err
		}
	}
	if script {
		if err := b.scriptSource(st, r); err != nil {
			return nil, "", err
		}
	} else if err := b.commandLine(st, k, r); err != nil {
		return nil, "", err
	}
	if t, ok := textOf(r.Take("workingDir")); ok && t != workspaceMount {
		var err error
		if st.WorkDir, err = b.tx.template(t); err != nil {
			return nil, "", err
		}
	}
	if ev, ok := r.Take("env"); ok {
		vars, from, err := b.envList(ev)
		if err != nil {
			return nil, "", err
		}
		st.Env = vars
		if len(from.Order) > 0 {
			k.Keep("valueFrom", from, caps.StepEnv, "env entries with valueFrom have no Value form; kept in Ext", r.Line("env"))
		}
	}
	if v, ok := r.Take("volumeMounts"); ok {
		b.keepMounts(k, v, r.Line("volumeMounts"))
	}
	k.KeepRest(r)
	st.Ext = k.Ext()
	return st, image, nil
}

// scriptSource reads source as script lines and command as the shell.
func (b *jobBuilder) scriptSource(st *ir.Step, r *reader) error {
	if cmd, ok := r.Take("command"); ok {
		words, isList := textList(cmd)
		if !isList {
			return fmt.Errorf("command must be a list of strings")
		}
		st.Shell = strings.Join(words, " ")
	}
	src, ok := textOf(r.Take("source"))
	if !ok {
		return nil
	}
	for _, l := range strings.Split(strings.TrimSuffix(src, "\n"), "\n") {
		tl, err := b.tx.template(l)
		if err != nil {
			return err
		}
		st.Script = append(st.Script, tl)
	}
	return nil
}

// commandLine writes command and args as one script line and keeps both lists in Ext, so the emitter can write
// them back while the line is unchanged.
func (b *jobBuilder) commandLine(st *ir.Step, k *sink, r *reader) error {
	cmd, hasCmd := r.Take("command")
	args, hasArgs := r.Take("args")
	if !hasCmd && !hasArgs {
		return nil
	}
	if lines, shell, ok := b.shellScript(cmd, args); ok {
		st.Script = lines
		if shell != shellCommand {
			st.Shell = shell
		}
		return nil
	}
	if lines, ok := b.wrappedScript(cmd, args); ok {
		st.Script = lines
		k.Out[ExtBashDefault] = true
		return nil
	}
	var words []string
	tagged := false
	for _, part := range []any{cmd, args} {
		if part == nil {
			continue
		}
		l, ok := textList(part)
		if !ok {
			return fmt.Errorf("command and args must be lists of strings")
		}
		for _, w := range l {
			tagged = tagged || strings.Contains(w, tagOpen)
			q, err := emitkit.ShellWord(b.tx.parts(w))
			if err != nil {
				return err
			}
			words = append(words, q)
		}
	}
	line := strings.Join(words, " ")
	st.Script = strings.Split(line, "\n")
	if tagged {
		// A tag reads an input or a parameter that the emitted template may not declare, so the lists cannot be written back.
		k.Note(caps.StepScript, "command and args are written as one script line; the lists hold a tag and are not kept", 0)
		return nil
	}
	k.Out[ExtScriptLine] = line
	k.Out[ExtCommand] = plain(cmd)
	k.Out[ExtArgs] = plain(args)
	k.Note(caps.StepScript, "command and args are written as one script line; both lists are kept in Ext", 0)
	return nil
}

// wrappedScript reads command [sh, -c] with the bash-if-present wrapper, its $0 and the script as the arguments.
func (b *jobBuilder) wrappedScript(cmd, args any) ([]string, bool) {
	c, okC := textList(cmd)
	a, okA := textList(args)
	if !okC || !okA || len(c) != 2 || c[0] != shellCommand || c[1] != "-c" || len(a) != 3 ||
		strings.TrimSuffix(a[0], "\n") != provider.BashOrShScript || a[1] != provider.BashOrShArgv0 {
		return nil, false
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSuffix(a[2], "\n"), "\n") {
		tl, err := b.tx.template(l)
		if err != nil {
			return nil, false
		}
		lines = append(lines, tl)
	}
	return lines, true
}

// shellScript reads command [shell, -ec] with one argument as the script lines that the emitter writes.
func (b *jobBuilder) shellScript(cmd, args any) (lines []string, shell string, ok bool) {
	c, okC := textList(cmd)
	a, okA := textList(args)
	if !okC || !okA || len(c) != 2 || (c[0] != shellCommand && !slices.Contains(posixShells, c[0])) || c[1] != shellFlags || len(a) != 1 {
		return nil, "", false
	}
	for _, l := range strings.Split(strings.TrimSuffix(a[0], "\n"), "\n") {
		tl, err := b.tx.template(l)
		if err != nil {
			return nil, "", false
		}
		lines = append(lines, tl)
	}
	return lines, c[0], true
}

// node is one container of a containerSet.
type node struct {
	st    *ir.Step
	name  string
	image string
	deps  []string
	line  int
}

// containerSet reads the containers into steps in dependency order.
func (b *jobBuilder) containerSet(r *reader) error {
	v, _ := r.Take(kindContainerSet)
	m, ok := asMap(v)
	if !ok {
		return fmt.Errorf("containerSet must be a mapping")
	}
	cr := newReader(m)
	cv, _ := cr.Take("containers")
	var nodes []*node
	for _, e := range listOf(cv) {
		cm, ok := asMap(e)
		name, _ := text(valueOf(cm, "name"))
		if !ok || name == "" {
			return fmt.Errorf("a container needs a name")
		}
		nr := newReader(cm)
		nr.Take("name")
		n := &node{name: name, line: cm.Lines["name"]}
		if dv, ok := nr.Take(keyDeps); ok {
			var isList bool
			if n.deps, isList = textList(dv); !isList {
				return fmt.Errorf("container %s: dependencies must be a list of names", name)
			}
		}
		var err error
		id := name
		if mk := cm.Marks["name"]; mk != "" {
			id = mk
		}
		if n.st, n.image, err = b.stepOf(id, nr, false); err != nil {
			return fmt.Errorf("container %s: %w", name, err)
		}
		if b.holds == nil {
			b.holds = map[string]string{}
		}
		b.holds[id] = canonJSON(plain(cm.Subset(restKeys(cm, keyDeps))))
		nodes = append(nodes, n)
	}
	order, err := sortNodes(nodes)
	if err != nil {
		return err
	}
	b.placeNodes(order)
	mounts, hasMounts := m.Vals["volumeMounts"]
	b.cacheMounts = hasMounts && b.isCacheMounts(mounts)
	ownMounts := hasMounts && (isWorkspaceMounts(mounts) || b.cacheMounts)
	if ownMounts {
		cr.Take("volumeMounts")
	}
	if hasMounts && !ownMounts && b.allMapped(mounts) && len(restKeys(m, "containers", "volumeMounts")) == 0 {
		cr.Take("volumeMounts")
		b.k.Out[kindContainerSet] = plain(m.Subset([]string{"volumeMounts"}))
	} else if rest := restKeys(m, "containers", "volumeMounts"); len(rest) > 0 || (hasMounts && !ownMounts) {
		rest = restKeys(m, "containers")
		for _, key := range rest {
			cr.Take(key)
		}
		b.k.Keep(kindContainerSet, m.Subset(rest), caps.ExtPassthrough, "containerSet keys beside containers (retryStrategy, volumeMounts) have no IR field; kept in Ext", r.Line(kindContainerSet))
	}
	return nil
}

// sortNodes orders the nodes so that each follows its dependencies; nodes that are ready keep their source order.
func sortNodes(nodes []*node) ([]*node, error) {
	byName := map[string]*node{}
	for _, n := range nodes {
		if byName[n.name] != nil {
			return nil, fmt.Errorf("container %q is defined twice", n.name)
		}
		byName[n.name] = n
	}
	placed := map[string]bool{}
	var out []*node
	for len(out) < len(nodes) {
		progress := false
		for _, n := range nodes {
			if placed[n.name] || !allPlaced(n, byName, placed) {
				continue
			}
			placed[n.name] = true
			out = append(out, n)
			progress = true
			break
		}
		if !progress {
			return nil, fmt.Errorf("containerSet dependencies hold a cycle")
		}
	}
	return out, nil
}

func allPlaced(n *node, byName map[string]*node, placed map[string]bool) bool {
	for _, d := range n.deps {
		if byName[d] == nil {
			return true // reported by placeNodes
		}
		if !placed[d] {
			return false
		}
	}
	return true
}

// placeNodes adds the steps. A graph that is not one chain keeps each dependency list in the step Ext.
func (b *jobBuilder) placeNodes(order []*node) {
	chain := true
	for i, n := range order {
		if i == 0 {
			chain = chain && len(n.deps) == 0
		} else {
			chain = chain && len(n.deps) == 1 && n.deps[0] == order[i-1].name
		}
	}
	images := map[string]bool{}
	for _, n := range order {
		images[n.image] = true
	}
	if len(images) == 1 {
		b.job.Container = imageOf(order[0].image)
	}
	for _, n := range order {
		if len(images) > 1 {
			n.st.Image = imageOf(n.image)
		}
		if !chain {
			deps := make([]any, len(n.deps))
			for i, d := range n.deps {
				deps[i] = d
			}
			if n.st.Ext == nil {
				n.st.Ext = ir.Ext{dialect: map[string]any{}}
			}
			n.st.Ext[dialect].(map[string]any)[keyDeps] = deps
		}
		b.job.Steps = append(b.job.Steps, n.st)
	}
	if !chain {
		b.k.Note(caps.StepScript, "the containerSet dependencies are not a chain, so the IR runs the steps in a sequence; each dependency list is kept in the step Ext", 0)
	}
}

// taskStep keeps a template with no IR form as one task step with the whole template in Ext.
func (b *jobBuilder) taskStep(r *reader, kind string) {
	rest := r.Rest()
	if kind == "" && len(rest) == 0 {
		return
	}
	sub := r.M.Subset(rest)
	for _, key := range rest {
		r.Take(key)
	}
	k := newSink(b.c.p.rep, b.c.file, b.path+"/step/"+mainStep)
	k.Keep(ExtTemplate, sub, caps.StepAction, kind+" template has no IR form; it is kept in Ext as one task step", r.Line(kind))
	b.job.Steps = append(b.job.Steps, &ir.Step{ID: mainStep, Kind: ir.StepTask, Ext: k.Ext()})
}

// isWorkspaceMounts is true for the one mount that the emitter writes on every containerSet.
func isWorkspaceMounts(v any) bool {
	l := listOf(v)
	if len(l) != 1 {
		return false
	}
	m, ok := asMap(l[0])
	n, _ := text(valueOf(m, "name"))
	p, _ := text(valueOf(m, "mountPath"))
	return ok && len(m.Order) == 2 && n == workspaceName && p == workspaceMount
}

// isWorkspaceVolumes is true for the one emptyDir volume that the emitter writes on every template.
func isWorkspaceVolumes(v any) bool {
	l := listOf(v)
	if len(l) != 1 {
		return false
	}
	m, ok := asMap(l[0])
	n, _ := text(valueOf(m, "name"))
	e, isMap := asMap(valueOf(m, "emptyDir"))
	return ok && len(m.Order) == 2 && n == workspaceName && isMap && len(e.Order) == 0
}

// withoutWorkspace drops the workspace volume that the emitter writes before the volumes of Ext.
func withoutWorkspace(v any) any {
	l := listOf(v)
	if len(l) > 1 && isWorkspaceVolumes([]any{l[0]}) {
		return l[1:]
	}
	return v
}

// uniqueStepIDs numbers a repeated step ID, such as a second checkout, because the IR needs one ID per step.
func (b *jobBuilder) uniqueStepIDs() {
	seen := map[string]bool{}
	for _, st := range b.job.Steps {
		id := st.ID
		for n := 2; seen[id]; n++ {
			id = st.ID + "-" + strconv.Itoa(n)
		}
		if id != st.ID {
			b.c.p.rep.Add(report.Entry{
				IRPath: b.path + "/step/" + id, Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "renamed",
				SourceLoc: b.c.file, Detail: "the step ID " + st.ID + " repeats in the job; the step ID is " + id,
			})
			st.ID = id
		}
		seen[id] = true
	}
}
