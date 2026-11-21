package tekton

import (
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	noopScript   = "true"
	noopStepName = "noop"
	outputsStep  = "pipemaster-outputs"
	saveStep     = "pipemaster-artifacts-save"
	restoreStep  = "pipemaster-artifacts-restore"
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
			b.nodes = append(b.nodes, m.n)
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
		tc.e.emulate(jp.path, caps.StepScript, "placeholder step", "the job has no step and a Task needs one; a step that runs "+noopScript+" stands in")
		m := newMap()
		m.setStr("name", noopStepName)
		m.setStr("image", firstNonEmpty(tc.containerRef, defaultImage))
		m.setStr("script", noopScript)
		b.nodes = append(b.nodes, m.n)
	}
	task, err := tc.taskDoc(b)
	if err != nil {
		return nil, err
	}
	tc.doc.root = task.root
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

// remoteTask writes a job that came from a remote taskRef back as a PipelineTask with that reference.
func (e *emitter) remoteTask(pp *plan, jp *jobPlan) (*wmap, bool) {
	j := jp.j
	if !e.own || len(j.Steps) != 1 || j.Steps[0].Kind != ir.StepTask {
		return nil, false
	}
	ref, ok := extOf(j.Steps[0].Ext)["taskRef"]
	if !ok {
		return nil, false
	}
	pt := newMap()
	pt.setStr("name", jp.tname)
	n, err := plainNode(ref)
	if err != nil {
		return nil, false
	}
	pt.set("taskRef", n)
	if v, ok := extOf(j.Ext)["params"]; ok {
		if pn, err := plainNode(v); err == nil {
			pt.set("params", pn)
		}
	}
	e.runAfter(pp, jp, pt, nil)
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
	m.setStr("name", name)
	m.setStr("image", firstNonEmpty(tc.containerRef, tc.image, defaultImage))
	m.set("script", block(script))
	return m.n, script
}

// outputStep writes the values of the job outputs to the result files, in one step after the others.
func (tc *taskCtx) outputStep(b *stepBook) error {
	j := tc.jp.j
	var lines []string
	sc := newScope()
	for _, o := range j.Outputs {
		if o.Value == "" {
			continue
		}
		line, err := tc.scriptLine(sc, tc.jp.path, "printf '%s' "+shq(o.Value)+" > "+shq("$(results."+o.Name+".path)"))
		if err != nil {
			return err
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	tc.e.emulate(tc.jp.path, caps.JobOutputs, "result step", "an output with a value is written to its result file by the step "+outputsStep)
	m := newMap()
	m.setStr("name", outputsStep)
	m.setStr("image", firstNonEmpty(tc.containerRef, tc.image, defaultImage))
	m.set("script", block(strings.Join(lines, "\n")))
	if len(sc.env) > 0 {
		m.set("env", seqOf(sc.env...))
	}
	b.nodes = append(b.nodes, m.n)
	return nil
}

// taskDoc assembles the Task document.
func (tc *taskCtx) taskDoc(b *stepBook) (*outDoc, error) {
	j := tc.jp.j
	root := newMap()
	root.setStr("apiVersion", apiPipelinesV1)
	root.setStr("kind", "Task")
	meta := newMap()
	meta.setStr("name", tc.jp.kname)
	if x := extOf(j.Ext); tc.e.own {
		if md, ok := x["task_metadata"].(map[string]any); ok {
			_ = meta.addExtra(md, nil)
		}
	}
	root.set("metadata", meta.n)
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
		n, _ := plainNode(v)
		spec.set("description", n)
	}
	spec.set("params", tc.paramNodes(x))
	spec.set("stepTemplate", tpl)
	spec.set("steps", seqOf(b.nodes...))
	spec.set("sidecars", sidecars)
	spec.set("results", tc.resultNodes(x))
	spec.set("workspaces", tc.workspaceNodes(x))
	root.set("spec", spec.n)
	return &outDoc{root: root.n}, nil
}

func (tc *taskCtx) paramNodes(x map[string]any) *yaml.Node {
	out := seqOf()
	for _, p := range tc.params {
		m := newMap()
		m.setStr("name", p)
		out.Content = append(out.Content, m.n)
	}
	if kept, ok := x["task_params"].([]any); ok {
		for _, k := range kept {
			if n, err := plainNode(k); err == nil {
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
		if n, err := plainNode(raw); err == nil {
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
		m.setStr("name", o.Name)
		out.Content = append(out.Content, m.n)
	}
	return out
}

func (tc *taskCtx) workspaceNodes(x map[string]any) *yaml.Node {
	out := seqOf()
	if raw, ok := x["task_workspaces"].([]any); ok {
		for _, r := range raw {
			if n, err := plainNode(r); err == nil {
				out.Content = append(out.Content, n)
			}
		}
	}
	if tc.ws {
		m := newMap()
		m.setStr("name", workspaceName)
		m.setStr("mountPath", workspaceMount)
		out.Content = append(out.Content, m.n)
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
		m.setStr("image", tc.containerRef)
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
	for _, n := range pipeEnv {
		if !taken[envName(n)] {
			env = append(env, n)
		}
	}
	env = append(env, jobEnv...)
	if len(env) > 0 {
		m.set("env", seqOf(env...))
	}
	if tc.checkout {
		m.setStr("workingDir", workspaceDir)
	}
	if extra, ok := x["stepTemplate"].(map[string]any); ok {
		if err := m.addExtra(extra, nil); err != nil {
			return nil, err
		}
	}
	if m.empty() {
		return nil, nil
	}
	return m.n, nil
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
		m.setStr("name", sv.Name)
		m.setStr("image", img)
		env, err := tc.envList(sc, sp, sv.Env)
		if err != nil {
			return nil, err
		}
		if len(env) > 0 {
			m.set("env", seqOf(env...))
		}
		if ports := tc.ports(sp, sv.Ports); len(ports.Content) > 0 {
			m.set("ports", ports)
		}
		out.Content = append(out.Content, m.n)
	}
	if len(out.Content) == 0 {
		return nil, nil
	}
	return out, nil
}

// ports writes the container ports of a service; a port text with a host part keeps the container part.
func (tc *taskCtx) ports(sp string, ps []string) *yaml.Node {
	out := seqOf()
	for _, p := range ps {
		c := p
		if i := strings.LastIndex(p, ":"); i >= 0 {
			c = p[i+1:]
		}
		n, err := strconv.Atoi(c)
		if err != nil || n <= 0 {
			tc.e.lose(sp, caps.JobServices, "port "+p+" is not a container port number")
			continue
		}
		m := newMap()
		m.set("containerPort", intNode(n))
		out.Content = append(out.Content, m.n)
	}
	return out
}
