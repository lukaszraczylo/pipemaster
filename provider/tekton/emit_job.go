package tekton

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	noopScript   = "true"
	noopStepName = "noop"
	// placeholderStepID is the step ID that every provider reports for the step it adds to a job without steps.
	placeholderStepID = "script"
	outputsStep       = "pipemaster-outputs"
	outputEnvPrefix   = "PM_OUT_"
	saveStep          = "pipemaster-artifacts-save"
	restoreStep       = "pipemaster-artifacts-restore"
)

// workspaceArg carries the cache and artifact settings of a job whose files the shared workspace stands in for.
type workspaceArg struct {
	Workspace string        `json:"workspace"`
	Save      string        `json:"save,omitempty"`
	Restore   string        `json:"restore,omitempty"`
	Cache     []ir.Cache    `json:"cache,omitempty"`
	Artifacts []ir.Artifact `json:"artifacts,omitempty"`
	Needs     []string      `json:"needs,omitempty"`
}

// job writes the Task of a job and returns its PipelineTask. A job that is one remote task reference has no Task.
func (e *emitter) job(pp *plan, jp *jobPlan) (*wmap, error) {
	j := jp.j
	e.foreignExt(jp.path, j.Ext)
	if pt, ok := e.remoteTask(pp, jp); ok {
		return pt, nil
	}
	tc := e.newTaskCtx(pp, jp)
	tc.doc = &outDoc{}
	e.owners[jp.path] = tc.doc
	jc := tc.cond(jp.path, caps.JobIf, j.If, pipelineScope)
	b := &stepBook{guard: jc.guard, names: map[string]bool{}}
	if jc.guard != nil {
		if err := tc.markJobGuard(jp.path, j.If, jc.guard); err != nil {
			return nil, err
		}
	}
	if j.Container != nil && len(j.Container.Entrypoint) > 0 {
		tc.e.lose(jp.path, caps.JobContainer, "the container entrypoint has no Tekton form beside a script")
	}
	if j.Container != nil && j.Container.Ref != "" {
		ref, ok, err := tc.text(newScope(), jp.path, caps.JobContainer, valueText, j.Container.Ref)
		if err != nil {
			return nil, err
		}
		if ok {
			tc.containerRef = ref
		}
	}
	clone := 0
	for _, st := range j.Steps {
		if st == nil {
			continue
		}
		m, err := tc.step(b, st, jp.path+"/step/"+st.ID)
		if err != nil {
			return nil, err
		}
		if m != nil {
			b.nodes = append(b.nodes, m.N)
			if tc.checkout && clone == 0 {
				clone = len(b.nodes)
			}
		}
	}
	if err := tc.workspaceSteps(b, clone); err != nil {
		return nil, err
	}
	if err := tc.outputStep(b); err != nil {
		return nil, err
	}
	if len(b.nodes) == 0 {
		tc.e.lose(jp.path+"/step/"+placeholderStepID, caps.StepScript, "the job has no steps; a no-op step keeps the Task valid")
		tc.e.emulate(jp.path, caps.StepScript, "placeholder step", "the job has no step and a Task needs one; a step that runs "+noopScript+" stands in")
		image := firstNonEmpty(tc.containerRef, defaultImage)
		if err := tc.e.markArg(caps.StepScript, jp.path, imageArg{Image: image}); err != nil {
			return nil, err
		}
		m := newMap()
		m.SetStr("name", noopStepName)
		m.SetStr("image", image)
		lines, sc := []string{noopScript}, newScope()
		if b.guard != nil {
			var err error
			if lines, err = exprtekton.AddGuard(lines, b.guard); err != nil {
				return nil, err
			}
			guardEnv(sc, b.guard)
		}
		if len(lines) == 1 {
			m.SetStr("script", lines[0])
		} else {
			m.Set("script", block(strings.Join(lines, "\n")))
		}
		if len(sc.env) > 0 {
			m.Set("env", seqOf(sc.env...))
		}
		b.nodes = append(b.nodes, m.N)
	}
	task, err := tc.taskDoc(b)
	if err != nil {
		return nil, err
	}
	tc.doc.Root = task.Root
	e.add(tasksDir+jp.kname+".yaml", tc.doc)
	return tc.pipelineTask(jc)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// remoteTask writes a job that came from a remote or catalog taskRef back as a PipelineTask with that reference.
func (e *emitter) remoteTask(pp *plan, jp *jobPlan) (*wmap, bool) {
	j := jp.j
	if !e.own {
		return nil, false
	}
	ref, ok := extOf(j.Ext)[ExtCatalogRef]
	if !ok {
		if len(j.Steps) != 1 || j.Steps[0].Kind != ir.StepTask {
			return nil, false
		}
		if ref, ok = extOf(j.Steps[0].Ext)["taskRef"]; !ok {
			return nil, false
		}
	}
	pt := newMap()
	pt.SetStr("name", jp.tname)
	n := e.node(jp.path, "taskRef", ref)
	if n == nil {
		return nil, false
	}
	pt.Set("taskRef", n)
	if j.Name != "" {
		pt.SetStr("displayName", j.Name)
	}
	if v, ok := extOf(j.Ext)["params"]; ok {
		pt.Set("params", e.node(jp.path, "params", v))
	}
	tc := e.newTaskCtx(pp, jp)
	e.runAfter(pp, jp, pt, nil)
	jc := tc.cond(jp.path, caps.JobIf, j.If, pipelineScope)
	if jc.guard != nil {
		e.lose(jp.path, caps.JobIf, "the condition has no when form and a referenced task has no script to guard; the task never runs")
		jc = condition{disabled: true, when: exprtekton.DisabledWhen()}
	}
	if err := tc.pipelineTaskKeys(pt, jc); err != nil {
		e.lose(jp.path, caps.JobMatrix, err.Error())
	}
	return pt, true
}

// workspaceSteps adds the steps that copy artifacts through the shared workspace, and the marker that holds
// the job settings the workspace stands in for.
func (tc *taskCtx) workspaceSteps(b *stepBook, clone int) error {
	j := tc.jp.j
	var needs []string
	var restore []string
	for _, n := range j.Needs {
		if !n.Artifacts {
			continue
		}
		needs = append(needs, n.JobID)
		if nj, ok := tc.pp.byID[n.JobID]; ok {
			for _, a := range nj.j.Artifacts {
				restore = append(restore, downloadScript(artifactName(artifactKey(nj.j, a)), "")...)
			}
		}
	}
	if len(j.Cache)+len(j.Artifacts)+len(needs) == 0 {
		return nil
	}
	tc.useWorkspace()
	arg := workspaceArg{Workspace: workspaceName, Cache: j.Cache, Artifacts: j.Artifacts, Needs: needs}
	if len(restore) > 0 {
		step, script := tc.workspaceStep(restoreStep, restore)
		b.nodes, arg.Restore = insertAt(b.nodes, clone, step), script
	}
	var save []string
	for _, a := range j.Artifacts {
		save = append(save, uploadScript(artifactName(artifactKey(j, a)), a.Paths)...)
	}
	if len(save) > 0 {
		step, script := tc.workspaceStep(saveStep, save)
		b.nodes, arg.Save = append(b.nodes, step), script
	}
	first := true
	for _, f := range []struct {
		f  caps.Feature
		ok bool
	}{{caps.JobCache, len(j.Cache) > 0}, {caps.JobArtifacts, len(j.Artifacts) > 0}, {caps.JobNeedsArtifacts, len(needs) > 0}} {
		if !f.ok {
			continue
		}
		if first {
			first = false
			if err := tc.e.markArg(f.f, tc.jp.path, arg); err != nil {
				return err
			}
			continue
		}
		tc.e.mark(f.f, tc.jp.path)
	}
	if len(j.Cache) > 0 {
		tc.e.emulate(tc.jp.path, caps.JobCache, "shared workspace", "the workspace is mounted at "+workspaceMount+"; cache paths are not copied in or out and nothing persists between runs")
	}
	return nil
}

// artifactKey is the directory name of a job artifact: its name, or the job ID.
func artifactKey(j *ir.Job, a ir.Artifact) string {
	if a.Name != "" {
		return a.Name
	}
	return j.ID
}

func insertAt(l []*yaml.Node, i int, n *yaml.Node) []*yaml.Node {
	l = append(l, nil)
	copy(l[i+1:], l[i:])
	l[i] = n
	return l
}

// workspaceStep writes a step that moves files through the shared workspace.
func (tc *taskCtx) workspaceStep(name string, lines []string) (*yaml.Node, string) {
	script := strings.Join(append([]string{"set -e"}, lines...), "\n")
	m := newMap()
	m.SetStr("name", name)
	m.SetStr("image", firstNonEmpty(tc.containerRef, tc.image, defaultImage))
	m.Set("script", block(script))
	return m.N, script
}

// outputStep writes the values of the job outputs to the result files, in one step after the others.
func (tc *taskCtx) outputStep(b *stepBook) error {
	j := tc.jp.j
	var lines []string
	var held []heldOutput
	sc := newScope()
	for i, o := range j.Outputs {
		if o.Value == "" {
			continue
		}
		val, ok, err := tc.text(sc, tc.jp.path, caps.JobOutputs, valueText, o.Value)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		name := outputEnvPrefix + strconv.Itoa(i)
		sc.add(name, nameValue(name, val))
		lines = append(lines, "printf '%s' \"${"+name+"}\" > "+shq("$(results."+o.Name+".path)"))
		held = append(held, heldOutput{Name: o.Name, Env: name})
	}
	if len(lines) == 0 {
		return nil
	}
	tc.e.emulate(tc.jp.path, caps.JobOutputs, "result step", "an output with a value is written to its result file by the step "+outputsStep)
	m := newMap()
	m.SetStr("name", outputsStep)
	image := firstNonEmpty(tc.containerRef, tc.image, defaultImage)
	script := strings.Join(lines, "\n")
	m.SetStr("image", image)
	m.Set("script", block(script))
	if len(sc.env) > 0 {
		m.Set("env", seqOf(sc.env...))
	}
	b.nodes = append(b.nodes, m.N)
	return tc.e.markArg(caps.JobOutputs, tc.jp.path, outputsArg{Image: image, Script: script, Outputs: held})
}

// taskDoc assembles the Task document.
func (tc *taskCtx) taskDoc(b *stepBook) (*outDoc, error) {
	j := tc.jp.j
	root := newMap()
	root.SetStr("apiVersion", apiPipelinesV1)
	root.SetStr("kind", "Task")
	meta := newMap()
	meta.SetStr("name", tc.jp.kname)
	if x := extOf(j.Ext); tc.e.own {
		if md, ok := x["task_metadata"].(map[string]any); ok {
			_ = meta.AddExtra(md, nil)
		}
	}
	root.Set("metadata", meta.N)
	spec := newMap()
	x := map[string]any{}
	if tc.e.own {
		x = extOf(j.Ext)
	}
	tpl, err := tc.stepTemplate(x)
	if err != nil {
		return nil, err
	}
	sidecars, err := tc.sidecars()
	if err != nil {
		return nil, err
	}
	if v, ok := x["task_description"]; ok {
		spec.Set("description", tc.e.node(tc.jp.path, "task description", v))
	}
	_, body := tc.jobExt()
	spec.Set("params", tc.paramNodes(x))
	spec.Set("stepTemplate", tpl)
	spec.Set("steps", seqOf(b.nodes...))
	spec.Set("sidecars", sidecars)
	spec.Set("results", tc.resultNodes(x))
	spec.Set("workspaces", tc.workspaceNodes(x))
	for _, k := range sortedKeys(body) {
		spec.Set(k, tc.e.node(tc.jp.path, "Task key "+k, body[k]))
	}
	root.Set("spec", spec.N)
	return &outDoc{Root: root.N}, nil
}

func (tc *taskCtx) paramNodes(x map[string]any) *yaml.Node {
	out := seqOf()
	for _, p := range tc.params {
		m := newMap()
		m.SetStr("name", p)
		out.Content = append(out.Content, m.N)
	}
	if kept, ok := x["task_params"].([]any); ok {
		for _, k := range kept {
			if n := tc.e.node(tc.jp.path, "task params", k); n != nil {
				out.Content = append(out.Content, n)
			}
		}
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}

func (tc *taskCtx) resultNodes(x map[string]any) *yaml.Node {
	if raw, ok := x["results"]; ok {
		if n := tc.e.node(tc.jp.path, "results", raw); n != nil {
			return n
		}
	}
	j := tc.jp.j
	if len(j.Outputs) == 0 {
		return nil
	}
	out := seqOf()
	for _, o := range j.Outputs {
		m := newMap()
		m.SetStr("name", o.Name)
		out.Content = append(out.Content, m.N)
	}
	return out
}

func (tc *taskCtx) workspaceNodes(x map[string]any) *yaml.Node {
	out := seqOf()
	if raw, ok := x["task_workspaces"].([]any); ok {
		for _, r := range raw {
			if n := tc.e.node(tc.jp.path, "task workspaces", r); n != nil {
				out.Content = append(out.Content, n)
			}
		}
	}
	if tc.ws {
		m := newMap()
		m.SetStr("name", workspaceName)
		m.SetStr("mountPath", workspaceMount)
		out.Content = append(out.Content, m.N)
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}

// stepTemplate writes the container image and the env that every step inherits: the pipeline env, then the job env.
func (tc *taskCtx) stepTemplate(x map[string]any) (*yaml.Node, error) {
	j := tc.jp.j
	m := newMap()
	if tc.containerRef != "" {
		m.SetStr("image", tc.containerRef)
	}
	sc := newScope()
	jobEnv, err := tc.envList(sc, tc.jp.path, j.Env)
	if err != nil {
		return nil, err
	}
	var env []*yaml.Node
	taken := map[string]bool{}
	for _, n := range jobEnv {
		taken[envName(n)] = true
	}
	pipeEnv, err := tc.envList(sc, tc.pp.path, tc.pp.pl.Env)
	if err != nil {
		return nil, err
	}
	var copied []string
	for _, n := range pipeEnv {
		if !taken[envName(n)] {
			env = append(env, n)
			copied = append(copied, envName(n))
		}
	}
	if len(copied) > 0 {
		if err := tc.e.markArg(caps.PipelineEnv, tc.jp.path, copiedArg{Names: copied}); err != nil {
			return nil, err
		}
	}
	env = append(env, jobEnv...)
	if len(env) > 0 {
		m.Set("env", seqOf(env...))
	}
	if tc.checkout {
		m.SetStr("workingDir", workspaceDir)
	}
	if extra, ok := x["stepTemplate"].(map[string]any); ok {
		if err := m.AddExtra(extra, nil); err != nil {
			return nil, err
		}
	}
	if m.Empty() {
		return nil, nil
	}
	return m.N, nil
}

// sidecars writes the services as sidecars; a service reaches its ports at localhost.
func (tc *taskCtx) sidecars() (*yaml.Node, error) {
	j := tc.jp.j
	if len(j.Services) == 0 {
		return nil, nil
	}
	out := seqOf()
	for _, sv := range j.Services {
		sp := tc.jp.path + "/service/" + sv.Name
		sc := newScope()
		img, ok, err := tc.text(sc, sp, caps.JobServices, valueText, sv.Image.Ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		m := newMap()
		m.SetStr("name", sv.Name)
		m.SetStr("image", img)
		env, err := tc.envList(sc, sp, sv.Env)
		if err != nil {
			return nil, err
		}
		if len(env) > 0 {
			m.Set("env", seqOf(env...))
		}
		if ports := tc.ports(sp, sv.Ports); len(ports.Content) > 0 {
			m.Set("ports", ports)
		}
		out.Content = append(out.Content, m.N)
	}
	if len(out.Content) == 0 {
		return nil, nil
	}
	return out, nil
}

// ports writes the container ports of a service; a port text with a host part keeps the container part.
func (tc *taskCtx) ports(sp string, ps []string) *yaml.Node {
	out := seqOf()
	mapped := false
	for _, p := range ps {
		c := p
		if i := strings.LastIndex(p, ":"); i >= 0 {
			c = p[i+1:]
			mapped = true
		}
		n, err := strconv.Atoi(c)
		if err != nil || n <= 0 {
			tc.e.lose(sp, caps.JobServices, "port "+p+" is not a container port number")
			continue
		}
		m := newMap()
		m.Set("containerPort", intNode(n))
		out.Content = append(out.Content, m.N)
	}
	if mapped {
		if err := tc.e.markArg(caps.JobServices, sp, portsArg{Ports: ps}); err != nil {
			tc.e.lose(sp, caps.JobServices, err.Error())
		}
	}
	return out
}

// jobHandled lists the job Ext keys that the emitter writes in a place of its own.
var jobHandled = map[string]bool{
	ExtImplicitNeeds: true, ExtCatalogRef: true, "task_name": true, "matrix_include_names": true, "array_params": true, "unused_params": true,
	"params": true, "stepTemplate": true, "results": true, "task_metadata": true, "task_params": true, "task_workspaces": true,
	"task_description": true, "workspaces": true, "description": true,
}

// jobExt returns the Ext keys of the job: the PipelineTask keys, and the Task body keys without their prefix.
func (tc *taskCtx) jobExt() (pipelineTask, taskBody map[string]any) {
	pipelineTask, taskBody = map[string]any{}, map[string]any{}
	if !tc.e.own {
		return
	}
	for k, v := range extOf(tc.jp.j.Ext) {
		switch rest, isTask := strings.CutPrefix(k, taskKeyPrefix); {
		case jobHandled[k] || k == "sidecars":
		case isTask:
			taskBody[rest] = v
		default:
			pipelineTask[k] = v
		}
	}
	if _, ok := extOf(tc.jp.j.Ext)["sidecars"]; ok {
		tc.e.lose(tc.jp.path, caps.ExtPassthrough, "sidecar keys beside name, image, env and ports have no place in the emitted Tekton files")
	}
	return
}

// extraParams adds the PipelineTask params that the parser kept: array values and values that no text read.
func (tc *taskCtx) extraParams(params *yaml.Node) {
	x := extOf(tc.jp.j.Ext)
	if !tc.e.own {
		return
	}
	have := map[string]bool{}
	for _, p := range tc.params {
		have[p] = true
	}
	arrays, _ := x["array_params"].(map[string]any)
	for _, name := range sortedKeys(arrays) {
		if n := tc.e.node(tc.jp.path, "param "+name, arrays[name]); n != nil && !have[name] {
			m := newMap()
			m.SetStr("name", name)
			m.Set("value", n)
			params.Content = append(params.Content, m.N)
		}
	}
	unused, _ := x["unused_params"].(map[string]any)
	for _, name := range sortedKeys(unused) {
		if s, ok := unused[name].(string); ok && !have[name] {
			params.Content = append(params.Content, nameValue(name, s))
		}
	}
}

// pipelineWorkspaceBindings lists the workspace bindings of the PipelineTask: those of the Tekton file, then ours.
func (tc *taskCtx) pipelineWorkspaceBindings(x map[string]any) *yaml.Node {
	out := seqOf()
	if raw, ok := x["workspaces"].([]any); ok && tc.e.own {
		for _, r := range raw {
			if n := tc.e.node(tc.jp.path, "workspaces binding", r); n != nil {
				out.Content = append(out.Content, n)
			}
		}
	}
	if tc.ws {
		w := newMap()
		w.SetStr("name", workspaceName)
		w.SetStr("workspace", workspaceName)
		out.Content = append(out.Content, w.N)
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}

// markJobGuard records the job condition that the guard line of every script step stands for, and what the
// guard reads back as, so the parser can tell an unedited guard.
func (tc *taskCtx) markJobGuard(path string, orig, guard expr.Expr) error {
	lines, err := exprtekton.AddGuard([]string{"true"}, guard)
	if err != nil {
		return err
	}
	back := ""
	if _, e, ok := exprtekton.RemoveGuard(lines); ok {
		back = expr.String(e)
	}
	return tc.e.markArg(caps.JobIf, path, guardArg{If: expr.String(orig), Back: back})
}
