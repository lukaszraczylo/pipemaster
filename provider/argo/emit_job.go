package argo

import (
	"github.com/lukaszraczylo/pipemaster/provider/internal/ymap"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	keyInputsOut = "inputs"
	keySidecars  = "sidecars"
	gateSuffix   = "-gate"
	osArchLabel  = `^([a-z0-9]+)/([a-z0-9]+)$`
)

var osArch = regexp.MustCompile(osArchLabel)

// jobSkip lists the job Ext keys that the emitter writes through its own code.
var jobSkip = map[string]bool{
	ExtImplicitNeeds: true, ExtDepends: true, ExtGates: true, ExtArguments: true, keyInputs: true, keyOutputs: true,
	"sidecars": true, "continueOn": true, "templateRef": true, "withItems": true, "withParam": true, "withSequence": true,
	"retryStrategy": true, "timeout": true, "activeDeadlineSeconds": true, "containerSet": true, "hooks": true, "onExit": true,
	"argument_artifacts": true, "volumes": true, "nodeSelector": true,
}

// taskKeys are the job Ext keys that belong to the DAG task, not the template.
var taskKeys = map[string]bool{"hooks": true, "onExit": true}

// gateArg carries the approval fields of a suspend gate so the parser can put them back.
type gateArg struct {
	Environment *ir.Environment `json:"environment,omitempty"`
	Manual      bool            `json:"manual,omitempty"`
}

// jobOut holds the DAG tasks and the templates that one job produced.
type jobOut struct {
	tasks []*yaml.Node
	tmpls []*yaml.Node
}

// needsMain is true when the job reads or writes an artifact or an output, which Argo does only for the
// container named main.
func (e *emitter) needsMain(j *ir.Job) bool {
	if len(j.Outputs) > 0 || len(j.Artifacts) > 0 {
		return true
	}
	for _, n := range j.Needs {
		if n.Artifacts {
			return true
		}
	}
	for _, st := range j.Steps {
		if st == nil || st.Action == nil || st.Action.Kind != ir.ActionRepo {
			continue
		}
		with := map[string]string{}
		for k, v := range st.With {
			if s, ok := valueCanon(v); ok {
				with[k] = s
			}
		}
		if t, ok := actions.Lookup(*st.Action, dialect, with); ok {
			switch t.Kind {
			case actions.KindImplicit, actions.KindUpload, actions.KindDownload:
				return true
			}
		}
	}
	return false
}

// actionTarget resolves the registry entry of a repo action step.
func actionTarget(st *ir.Step) (actions.Target, bool) {
	if st.Action == nil || st.Action.Kind != ir.ActionRepo {
		return actions.Target{}, false
	}
	with := map[string]string{}
	for k, v := range st.With {
		if s, ok := valueCanon(v); ok {
			with[k] = s
		}
	}
	return actions.Lookup(*st.Action, dialect, with)
}

// runsContainer is true when the step becomes a container of the set.
func (e *emitter) runsContainer(st *ir.Step) bool {
	switch st.Kind {
	case ir.StepScript, "":
		return len(st.Script) > 0 || extOf(st.Ext)[ExtScriptLine] != nil
	case ir.StepAction:
		t, ok := actionTarget(st)
		return ok && t.Kind == actions.KindScript && t.Support.Level != caps.Lost
	}
	return false
}

// job writes the DAG task and the template of one job.
func (e *emitter) job(pp *plan, jp *jobPlan) (jobOut, error) {
	j, path := jp.j, jp.path
	tc := e.newJobCtx(pp, jp)
	jx := map[string]any{}
	if e.own {
		jx = extOf(j.Ext)
	}
	e.foreignExt(path, j.Ext)
	var out jobOut
	items, matrixOK, whole := (*yaml.Node)(nil), true, false
	if j.Matrix != nil {
		items, matrixOK, whole = tc.matrixItems()
	}
	cond := tc.condition()
	cond.disabled = cond.disabled || !matrixOK
	if cond.disabled {
		e.markDisabled(j, path)
	}
	e.jobFeatures(tc, jx)

	sx := &stepCtx{tc: tc, taken: map[string]bool{}, takenIn: map[string]bool{}, dests: map[string]bool{}}
	sx.needArtifacts()
	conts, taskStep := sx.containers(jx)
	tm := newMap()
	tm.SetStr("name", jp.tmpl)
	ref := false
	switch {
	case taskStep != nil && extOf(taskStep.Ext)[ExtTemplate] == nil:
		ref = true
	case taskStep != nil:
		e.taskTemplate(tc, tm, taskStep)
	default:
		e.containerTemplate(tc, tm, conts, jx)
	}
	e.templateKeys(tc, tm, sx, jx, taskStep != nil)
	d := e.finalDeps(tc, cond, jx)
	var gate string
	gate, d = e.gateTasks(tc, d, jx, &out)
	task := e.dagTask(tc, sx, cond, d, gate, items, whole, jx, ref)
	out.tasks = append(out.tasks, task.N)
	if !ref {
		out.tmpls = append(out.tmpls, tm.N)
	}
	tc.reportSafeTags()
	e.markJobEnv(j, jp.path)
	e.markOutputs(j, jp.path)
	e.markCache(j, jp.path)
	written := imageRef(j.Container)
	if len(conts) > 0 {
		written = conts[0].image
	}
	e.markContainer(j, jp.path, written, sx.image != "")
	e.markArtifacts(j, jp.path)
	if len(tc.vars) > 0 {
		if err := e.markArg(caps.ScriptInterpolation, jp.path, varsArg{Vars: tc.vars}); err != nil {
			e.lose(jp.path, caps.ScriptInterpolation, "the variable marker cannot be written: "+err.Error())
		}
	}
	return out, nil
}

// jobFeatures reports the job fields that Argo has no form for.
func (e *emitter) jobFeatures(tc *jobCtx, jx map[string]any) {
	j, path := tc.jp.j, tc.jp.path
	if _, ok := jx[ExtDepends].(string); ok {
		e.lose(path, caps.JobIf, "the depends text kept in Ext is written again from the condition of the job; an atom with no IR status is lost")
	}
	if j.Environment != nil && !j.Environment.Approval || (j.Environment != nil && (j.Environment.Name != "" || j.Environment.URL != "")) {
		e.lose(path, caps.JobEnvironment, "Argo has no environment name or URL; they stay in a comment marker only")
	}
	for _, n := range j.Needs {
		if n.Optional {
			e.lose(path, caps.JobNeeds, "the need "+n.JobID+" is optional, and Argo has no optional dependency; the task waits for it")
		}
	}
	if j.Retry != nil && len(j.Retry.On) > 0 {
		e.lose(path, caps.JobRetry, "retry conditions "+strings.Join(j.Retry.On, ", ")+" have no form; Argo retries on failure and error")
	}
	r := j.Runner
	if len(r.Labels) > 0 || r.Platform != "" || len(r.Selector) > 0 {
		e.lose(path, caps.JobRunner, "Argo schedules pods; a runner label has no form")
	}
}

// containers writes the steps. taskStep is the step that keeps a whole template of an Argo file.
func (sx *stepCtx) containers(jx map[string]any) (conts []*container, taskStep *ir.Step) {
	tc, e := sx.tc, sx.tc.e
	j := tc.jp.j
	var producing []*ir.Step
	for _, st := range j.Steps {
		if st != nil && e.runsContainer(st) {
			producing = append(producing, st)
		}
	}
	names, taken := stepNames(producing, e.needsMain(j), e, tc.jp.path)
	sx.nameTaken = taken
	for i, st := range j.Steps {
		if st == nil {
			continue
		}
		path := tc.jp.path + "/step/" + st.ID
		e.foreignExt(path, st.Ext)
		last := len(producing) > 0 && producing[len(producing)-1] == st
		switch st.Kind {
		case ir.StepAction:
			sx.act, sx.disabled = actionArg{}, false
			if c := sx.actionStep(st, names[st], last, len(conts)); c != nil {
				conts = append(conts, c)
			}
			sx.markAction(st)
		case ir.StepScript, "":
			if e.runsContainer(st) {
				sx.act, sx.disabled = actionArg{}, false
				before := e.nlost
				c := sx.containerFor(st, names[st], last, jx)
				conts = append(conts, c)
				if sx.disabled || e.nlost != before {
					sx.act.Hold = nodeCanon(c.m.N)
					sx.markAction(st)
				}
			} else {
				if len(st.Script) == 0 {
					e.lose(path, caps.StepScript, "the step has no script; no container runs it")
				}
				sx.act, sx.disabled = actionArg{}, false
				conts = append(conts, sx.placeholder(st))
				sx.markAction(st)
			}
		case ir.StepTask:
			if e.own && extOf(st.Ext)[ExtTemplate] != nil || jx["templateRef"] != nil {
				taskStep = j.Steps[i]
				continue
			}
			e.lose(path, caps.StepAction, "a task step has no Argo form; the step is dropped")
			sx.act, sx.disabled = actionArg{}, false
			conts = append(conts, sx.placeholder(st))
			sx.markAction(st)
		default:
			e.lose(path, caps.StepAction, "a "+string(st.Kind)+" step has no Argo form; the step is dropped")
			sx.act, sx.disabled = actionArg{}, false
			conts = append(conts, sx.placeholder(st))
			sx.markAction(st)
		}
	}
	return conts, taskStep
}

// taskTemplate writes the template of a task step that kept a whole Argo template.
func (e *emitter) taskTemplate(tc *jobCtx, tm *wmap, st *ir.Step) {
	if body, ok := extOf(st.Ext)[ExtTemplate].(map[string]any); ok {
		if err := tm.AddExtra(body, nil); err != nil {
			e.lose(tc.jp.path, caps.ExtPassthrough, "the template of the task step cannot be written: "+err.Error())
		}
	}
}

// containerTemplate writes the containerSet of the job.
func (e *emitter) containerTemplate(tc *jobCtx, tm *wmap, conts []*container, jx map[string]any) {
	path := tc.jp.path
	if len(conts) == 0 || (e.needsMain(tc.jp.j) && !slices.ContainsFunc(conts, func(c *container) bool { return c.name == mainStep })) {
		c := &container{name: mainStep, m: newMap(), image: defaultImage}
		c.m.SetStr("name", mainStep)
		c.m.SetStr("image", tc.jobImage())
		c.m.Set("command", strSeq([]string{shellCommand, shellFlags}))
		c.m.Set("args", seqOf(str("true")))
		c.m.SetStr("workingDir", workspaceMount)
		conts = append(conts, c)
		e.emulate(path, caps.StepScript, "no-op container", "the job runs no script, so a container named main carries its inputs and outputs")
		if err := e.markArg(caps.StepScript, path, struct{}{}); err != nil {
			e.lose(path, caps.StepScript, "the no-op marker cannot be written: "+err.Error())
		}
	}
	deps := chainDeps(conts)
	var nodes []*yaml.Node
	for i, c := range conts {
		if len(deps[i]) > 0 {
			c.m.Set("dependencies", strSeq(deps[i]))
		}
		nodes = append(nodes, c.m.N)
	}
	tm.Set("containerSet", e.containerSet(tc, nodes, jx))
}

// jobImage is the image of a container that no step names.
func (tc *jobCtx) jobImage() string {
	if c := tc.jp.j.Container; c != nil && c.Ref != "" {
		return c.Ref
	}
	return defaultImage
}

// chainDeps gives each container the dependencies that keep the step order: the previous container, or the
// dependencies that an Argo file kept in the step Ext.
func chainDeps(conts []*container) [][]string {
	out := make([][]string, len(conts))
	byID := map[string]string{}
	for _, c := range conts {
		if c.st != nil {
			byID[c.st.ID] = c.name
		}
	}
	kept := false
	for _, c := range conts {
		if c.st != nil && extOf(c.st.Ext)[keyDeps] != nil {
			kept = true
		}
	}
	for i, c := range conts {
		if kept && c.st != nil {
			if l, ok := extOf(c.st.Ext)[keyDeps].([]any); ok {
				for _, d := range l {
					if s, ok := d.(string); ok {
						out[i] = append(out[i], s)
					}
				}
			}
			continue
		}
		if i > 0 {
			out[i] = []string{conts[i-1].name}
		}
	}
	return out
}

// containerSet builds the containerSet map with the workspace mount and the cache mounts.
func (e *emitter) containerSet(tc *jobCtx, containers []*yaml.Node, jx map[string]any) *yaml.Node {
	cs := newMap()
	cs.Set("containers", seqOf(containers...))
	ext, _ := jx["containerSet"].(map[string]any)
	if mounts, ok := ext["volumeMounts"].([]any); ok && hasNamed(mounts, workspaceName) {
		cs.Set("volumeMounts", e.node(tc.jp.path, "volumeMounts", mounts))
	} else {
		var ms []*yaml.Node
		ms = append(ms, mountNode(workspaceName, workspaceMount, ""))
		ms = append(ms, tc.cacheMounts()...)
		if mounts, ok := ext["volumeMounts"].([]any); ok {
			for _, m := range mounts {
				ms = append(ms, e.node(tc.jp.path, "volumeMounts", m))
			}
		}
		cs.Set("volumeMounts", seqOf(ms...))
	}
	if err := cs.AddExtra(ext, map[string]bool{"volumeMounts": true}); err != nil {
		e.lose(tc.jp.path, caps.ExtPassthrough, "the containerSet keys of the Argo file cannot be written: "+err.Error())
	}
	return cs.N
}

func hasNamed(list []any, name string) bool {
	for _, e := range list {
		if m, ok := e.(map[string]any); ok && m["name"] == name {
			return true
		}
	}
	return false
}

func mountNode(name, path, sub string) *yaml.Node {
	m := newMap()
	m.SetStr("name", name)
	m.SetStr("mountPath", path)
	if sub != "" {
		m.SetStr("subPath", sub)
	}
	return m.N
}

// cacheMounts mounts the claim of each job cache.
func (tc *jobCtx) cacheMounts() []*yaml.Node {
	var out []*yaml.Node
	j, pp := tc.jp.j, tc.pp
	for i, c := range j.Cache {
		claim := "cache-" + tc.jp.task + "-" + itoa(i+1)
		if _, ok := pp.claims[claim]; !ok {
			pp.claims[claim] = cacheClaim{name: claim, size: "1Gi"}
			pp.claimOrder = append(pp.claimOrder, claim)
		}
		for k, p := range c.Paths {
			sub := "p" + itoa(k+1)
			out = append(out, mountNode(claim, artifactPath(strings.TrimPrefix(p, "~/")), sub))
		}
		tc.e.emulate(tc.jp.path, caps.JobCache, "PVC from volumeClaimTemplates", "the cache "+c.Key+" is a claim per job and holds the paths; it lives for one Workflow unless the claim is shared")
	}
	return out
}

// templateKeys adds inputs, outputs, timeout, retry, sidecars and the Ext keys to the template.
func (e *emitter) templateKeys(tc *jobCtx, tm *wmap, sx *stepCtx, jx map[string]any, whole bool) {
	j, path := tc.jp.j, tc.jp.path
	e.inputsKey(tc, tm, sx, jx)
	e.outputsKey(tc, tm, sx, jx)
	if j.Timeout != nil {
		tm.SetStr("timeout", time.Duration(*j.Timeout).String())
	} else if v, ok := jx["timeout"]; ok {
		tm.Set("timeout", e.node(path, "timeout", v))
	}
	if j.Retry != nil || jx["retryStrategy"] != nil {
		rs := newMap()
		limit := "0"
		if j.Retry != nil && j.Retry.Attempts > 1 {
			limit = itoa(j.Retry.Attempts - 1)
		}
		rs.SetStr("limit", limit)
		if x, ok := jx["retryStrategy"].(map[string]any); ok {
			_ = rs.AddExtra(x, nil)
		}
		if j.Retry != nil || len(rs.N.Content) > 2 {
			tm.Set("retryStrategy", rs.N)
		}
	}
	if sc := e.sidecars(tc, jx); sc != nil {
		tm.Set(keySidecars, sc)
	}
	if !whole {
		vols := []any{map[string]any{"name": workspaceName, "emptyDir": map[string]any{}}}
		if ext, ok := jx["volumes"].([]any); ok {
			if hasNamed(ext, workspaceName) {
				vols = nil
			}
			vols = append(vols, ext...)
		}
		tm.Set("volumes", e.node(path, "volumes", vols))
	}
	skip := map[string]bool{}
	for k := range jobSkip {
		skip[k] = true
	}
	rest := map[string]any{}
	for k, v := range jx {
		if !skip[k] && !taskKeys[k] && !strings.HasPrefix(k, "arguments_") {
			rest[k] = v
		}
	}
	if ns, ok := jx["nodeSelector"]; ok {
		rest["nodeSelector"] = ns
	}
	if err := tm.AddExtra(rest, nil); err != nil {
		e.lose(path, caps.ExtPassthrough, "the template keys of the Argo file cannot be written: "+err.Error())
	}
}

func (e *emitter) inputsKey(tc *jobCtx, tm *wmap, sx *stepCtx, jx map[string]any) {
	ext, _ := jx[keyInputs].(map[string]any)
	in := newMap()
	var params []*yaml.Node
	seen := map[string]bool{}
	for _, p := range tc.params {
		seen[p.name] = true
		m := newMap()
		m.SetStr("name", p.name)
		params = append(params, m.N)
	}
	if l, ok := ext["parameters"].([]any); ok {
		for _, p := range l {
			if m, ok := p.(map[string]any); ok && !seen[strOf(m["name"])] {
				params = append(params, e.node(tc.jp.path, "inputs", p))
			}
		}
	}
	if len(params) > 0 {
		in.Set("parameters", seqOf(params...))
	}
	arts := append([]*yaml.Node{}, sx.artIn...)
	if l, ok := ext["artifacts"].([]any); ok {
		written := map[string]bool{}
		for _, a := range sx.artIn {
			written[nodeName(a)] = true
		}
		for _, a := range l {
			if m, isMap := a.(map[string]any); isMap && written[strOf(m["name"])] {
				continue
			}
			arts = append(arts, e.node(tc.jp.path, "inputs", a))
		}
	}
	if len(arts) > 0 {
		in.Set("artifacts", seqOf(arts...))
	}
	rest := map[string]any{}
	for k, v := range ext {
		if k != "parameters" && k != "artifacts" {
			rest[k] = v
		}
	}
	_ = in.AddExtra(rest, nil)
	if !in.Empty() {
		tm.Set(keyInputsOut, in.N)
	}
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func (e *emitter) outputsKey(tc *jobCtx, tm *wmap, sx *stepCtx, jx map[string]any) {
	j, path := tc.jp.j, tc.jp.path
	ext, _ := jx[keyOutputs].(map[string]any)
	out := newMap()
	extParams, _ := ext["parameters"].(map[string]any)
	var params []*yaml.Node
	for _, o := range j.Outputs {
		if !paramNameRE.MatchString(o.Name) {
			e.lose(path, caps.JobOutputs, "the output name "+o.Name+" is no valid parameter name; the output is not written")
			continue
		}
		m := newMap()
		m.SetStr("name", o.Name)
		if rest, ok := extParams[o.Name].(map[string]any); ok {
			_ = m.AddExtra(rest, nil)
		} else {
			vf := newMap()
			vf.SetStr("path", outputFile(o.Name))
			vf.SetStr("default", "")
			m.Set("valueFrom", vf.N)
		}
		params = append(params, m.N)
	}
	if len(params) > 0 {
		out.Set("parameters", seqOf(params...))
	}
	for _, a := range j.Artifacts {
		sx.addOutputArtifacts(path, safeName(a.Name), a.Paths, a.Expire)
	}
	extArts, _ := ext["artifacts"].(map[string]any)
	if len(sx.artOut) > 0 {
		for _, a := range sx.artOut {
			if rest, ok := extArts[a.Content[1].Value].(map[string]any); ok {
				mm := ymap.Of(a, "name", "path")
				_ = mm.AddExtra(rest, nil)
			}
		}
		out.Set("artifacts", seqOf(sx.artOut...))
	}
	rest := map[string]any{}
	for k, v := range ext {
		if k != "parameters" && k != "artifacts" {
			rest[k] = v
		}
	}
	_ = out.AddExtra(rest, nil)
	if !out.Empty() {
		tm.Set(keyOutputs, out.N)
	}
}

// sidecars writes the services of the job.
func (e *emitter) sidecars(tc *jobCtx, jx map[string]any) *yaml.Node {
	j, path := tc.jp.j, tc.jp.path
	if len(j.Services) == 0 {
		return nil
	}
	ext, _ := jx[keySidecars].(map[string]any)
	var out []*yaml.Node
	for _, sv := range j.Services {
		m := newMap()
		m.SetStr("name", sv.Name)
		if sv.Image.Ref != "" {
			if img, ok := tc.textOrLose(path, caps.JobServices, "the image of service "+sv.Name, sv.Image.Ref); ok {
				m.SetStr("image", img)
			}
		}
		if env := tc.envList(newScope(), path, sv.Env); len(env) > 0 {
			m.Set("env", seqOf(env...))
		}
		var ports []*yaml.Node
		for _, p := range sv.Ports {
			port := newMap()
			n, err := portNumber(p)
			if err != "" {
				e.lose(path, caps.JobServices, "the port "+p+" of service "+sv.Name+" "+err)
				continue
			}
			port.Set("containerPort", intNode(n))
			ports = append(ports, port.N)
		}
		if len(ports) > 0 {
			m.Set("ports", seqOf(ports...))
		}
		e.markService(path, sv)
		if rest, ok := ext[sv.Name].(map[string]any); ok {
			_ = m.AddExtra(rest, nil)
		}
		out = append(out, m.N)
		e.emulate(path, caps.JobServices, "template sidecar", "service "+sv.Name+" is reachable at localhost, not at its name")
	}
	return seqOf(out...)
}

func portNumber(p string) (int, string) {
	host, _, _ := strings.Cut(p, ":")
	n := 0
	for _, r := range host {
		if r < '0' || r > '9' {
			return 0, "is not a number"
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 || n > 65535 {
		return 0, "is out of range"
	}
	return n, ""
}

var _ = slices.Contains[[]string]

// serviceArg holds what the sidecar of a service cannot carry: the host ports and the image entrypoint.
type serviceArg struct {
	Ports      []string `json:"ports,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	// Image is the image reference that the sidecar was written with.
	Image string `json:"image,omitempty"`
}

func (e *emitter) markService(path string, sv ir.Service) {
	var a serviceArg
	if slices.ContainsFunc(sv.Ports, func(p string) bool { return strings.Contains(p, ":") }) {
		a.Ports = sv.Ports
	}
	if len(sv.Image.Entrypoint) > 0 {
		a.Entrypoint, a.Image = sv.Image.Entrypoint, sv.Image.Ref
		e.lose(path, caps.JobServices, "the entrypoint of service "+sv.Name+" has no Argo form; the sidecar runs the image entrypoint")
	}
	if a.Ports == nil && a.Entrypoint == nil {
		return
	}
	if err := e.markArg(caps.JobServices, path+"/service/"+sv.Name, a); err != nil {
		e.lose(path, caps.JobServices, "the service marker cannot be written: "+err.Error())
	}
}
