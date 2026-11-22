package argo

import (
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
)

// deps is how a task waits: dependencies, or a depends expression.
type deps struct {
	depends string
	names   []string
}

// reaches is true when job id is an ancestor of the job jp.
func (pp *plan) reaches(from *jobPlan, id string) bool {
	return slices.ContainsFunc(pp.ancestors(from.j), func(a *jobPlan) bool { return a.j.ID == id })
}

// finalDeps settles the dependencies of a task: the needs, the jobs that an output reference reads, and no
// need that only came from a reference and that another need already reaches.
func (e *emitter) finalDeps(tc *jobCtx, cond taskCond, jx map[string]any) deps {
	d := deps{depends: cond.depends, names: slices.Clone(cond.deps)}
	pp, j := tc.pp, tc.jp.j
	needs := map[string]bool{}
	for _, n := range j.Needs {
		needs[n.JobID] = true
	}
	if implicit, ok := jx[ExtImplicitNeeds].([]any); ok {
		for _, v := range implicit {
			id, _ := v.(string)
			jj, known := pp.byID[id]
			if !known || cond.depends != "" {
				continue
			}
			for _, n := range j.Needs {
				if n.JobID != id && pp.byID[n.JobID] != nil && pp.reaches(pp.byID[n.JobID], id) {
					d.names = slices.DeleteFunc(d.names, func(s string) bool { return s == jj.task })
					break
				}
			}
		}
	}
	for _, jj := range pp.jobs {
		if !tc.needed[jj.j.ID] || needs[jj.j.ID] {
			continue
		}
		switch {
		case d.depends != "":
			d.depends += " && " + jj.task + "." + atomSucceeded
		case !slices.Contains(d.names, jj.task):
			d.names = append(d.names, jj.task)
		}
	}
	return d
}

// gateTasks writes the suspend task that holds a manual job or an approval. It returns the name of the first
// gate, which the job waits for, and puts the dependencies on the gate.
func (e *emitter) gateTasks(tc *jobCtx, d deps, jx map[string]any, out *jobOut) (string, deps) {
	j, path := tc.jp.j, tc.jp.path
	approval := j.Environment != nil && j.Environment.Approval
	list, _ := jx[ExtGates].([]any)
	if !j.Manual && !approval && len(list) == 0 {
		return "", d
	}
	if d.depends != "" {
		e.lose(path, caps.JobManual, "the task holds a depends expression, which a gate cannot carry; the gate is not written and the job runs with no approval")
		return "", d
	}
	feature := caps.JobManual
	if approval {
		feature = caps.JobEnvironmentApproval
	}
	e.note(path, feature, caps.Native, "suspend template as a gate task", "anyone with RBAC to resume the Workflow can approve")
	arg := gateArg{Manual: j.Manual}
	if approval {
		env := *j.Environment
		arg.Environment = &env
	}
	if err := e.markArg(feature, path, arg); err != nil {
		e.lose(path, feature, "the gate marker cannot be written: "+err.Error())
	}
	type gate struct {
		body any
		task string
		tmpl string
	}
	var gates []gate
	if e.own && len(list) > 0 {
		for _, g := range list {
			m, _ := g.(map[string]any)
			gates = append(gates, gate{task: strOf(m["task"]), tmpl: strOf(m["template"]), body: m["body"]})
		}
	} else {
		name := unique(tc.pp.taskNames, tc.jp.task+gateSuffix, maxName)
		tm := unique(tc.pp.tmpls, name, maxName)
		gates = append(gates, gate{task: name, tmpl: tm})
	}
	deps := d
	for i, g := range gates {
		t := newMap()
		t.setStr("name", g.task)
		t.setStr("template", g.tmpl)
		next := deps.names
		if i+1 < len(gates) {
			next = []string{gates[i+1].task}
		}
		if len(next) > 0 {
			t.set("dependencies", strSeq(next))
		}
		out.tasks = append(out.tasks, t.n)
		tmpl := newMap()
		if body, ok := g.body.(map[string]any); ok {
			_ = tmpl.addExtra(body, nil)
		} else {
			tmpl.setStr("name", g.tmpl)
			tmpl.set("suspend", newMap().n)
		}
		out.tmpls = append(out.tmpls, tmpl.n)
	}
	return gates[0].task, deps
}

// dagTask writes the DAG task of a job.
func (e *emitter) dagTask(tc *jobCtx, sx *stepCtx, cond taskCond, d deps, gate string, items *yaml.Node, whole bool, jx map[string]any, ref bool) *wmap {
	j, jp, path := tc.jp.j, tc.jp, tc.jp.path
	t := newMap()
	t.setStr("name", jp.task)
	comments := []string{}
	if jp.task != j.ID {
		comments = append(comments, idComment(j.ID))
	}
	if j.Name != "" {
		comments = append(comments, "# "+commentSafe(j.Name))
	}
	if len(comments) > 0 {
		t.keyComment("name", strings.Join(comments, "\n"))
	}
	if ref {
		t.set("templateRef", e.node(path, "templateRef", jx["templateRef"]))
	} else {
		t.setStr("template", jp.tmpl)
	}
	switch {
	case gate != "" && d.depends == "":
		t.set("dependencies", strSeq([]string{gate}))
	case d.depends != "":
		t.setStr("depends", d.depends)
	case len(d.names) > 0:
		t.set("dependencies", strSeq(d.names))
	}
	switch {
	case cond.disabled:
		t.setStr("when", disabledWhen())
	case cond.when != "":
		t.setStr("when", cond.when)
	}
	if j.AllowFailure || jx["continueOn"] != nil {
		co := newMap()
		if j.AllowFailure {
			co.set("failed", boolNode(true))
		}
		if x, ok := jx["continueOn"].(map[string]any); ok {
			_ = co.addExtra(x, nil)
		}
		if !co.empty() {
			t.set("continueOn", co.n)
		}
	}
	switch {
	case items != nil && !cond.disabled:
		t.set("withItems", items)
		if whole {
			if err := e.markArg(caps.JobMatrix, path, argOf(j.Matrix)); err != nil {
				e.lose(path, caps.JobMatrix, "the matrix marker cannot be written: "+err.Error())
			}
		}
	default:
		for _, k := range []string{"withItems", "withParam", "withSequence"} {
			if v, ok := jx[k]; ok && j.Matrix == nil {
				t.set(k, e.node(path, k, v))
			}
		}
	}
	t.set("arguments", e.taskArguments(tc, sx, jx))
	if t.has("arguments") && t.n.Content[len(t.n.Content)-1].Kind == yaml.MappingNode && len(t.n.Content[len(t.n.Content)-1].Content) == 0 {
		t.n.Content = t.n.Content[:len(t.n.Content)-2]
		delete(t.keys, "arguments")
	}
	rest := map[string]any{}
	for k := range taskKeys {
		if v, ok := jx[k]; ok {
			rest[k] = v
		}
	}
	_ = t.addExtra(rest, nil)
	return t
}

// taskArguments binds the template inputs: the loop item, the outputs of other tasks, the artifacts, and the
// arguments that an Argo file kept.
func (e *emitter) taskArguments(tc *jobCtx, sx *stepCtx, jx map[string]any) *yaml.Node {
	path := tc.jp.path
	args := newMap()
	var params []*yaml.Node
	bound := map[string]bool{}
	for _, p := range tc.params {
		bound[p.name] = true
		params = append(params, nameValue(p.name, p.value))
	}
	if kept, ok := jx[ExtArguments].(map[string]any); ok {
		for _, name := range sortedKeys(kept) {
			if bound[name] {
				continue
			}
			if v, isText := kept[name].(string); isText {
				params = append(params, nameValue(name, v))
				continue
			}
			params = append(params, e.node(path, "arguments", kept[name]))
		}
	}
	if len(params) > 0 {
		args.set("parameters", seqOf(params...))
	}
	arts := append([]*yaml.Node{}, sx.argArts...)
	if l, ok := jx["argument_artifacts"].([]any); ok {
		for _, a := range l {
			arts = append(arts, e.node(path, "arguments", a))
		}
	}
	if len(arts) > 0 {
		args.set("artifacts", seqOf(arts...))
	}
	rest := map[string]any{}
	for k, v := range jx {
		if strings.HasPrefix(k, "arguments_") {
			rest[strings.TrimPrefix(k, "arguments_")] = v
		}
	}
	_ = args.addExtra(rest, nil)
	return args.n
}
