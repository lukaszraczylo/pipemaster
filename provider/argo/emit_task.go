package argo

import (
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
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
	if tc.jp.finally {
		d = e.exitDeps(tc, d)
	}
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

// dependsAtom matches one task of a depends expression, with the optional status after a dot.
var dependsAtom = regexp.MustCompile(`([A-Za-z0-9_-]+)(?:\.[A-Za-z]+)?`)

// dependsTasks lists the task names that a depends expression reads. A name that ends another name is no match.
func dependsTasks(depends string) []string {
	var out []string
	for _, m := range dependsAtom.FindAllStringSubmatch(depends, -1) {
		out = append(out, m[1])
	}
	return out
}

// exitDeps limits the dependencies of an exit handler task to the tasks of the exit handler. Argo runs the
// handler after the whole entry DAG, and a DAG cannot name a task of another DAG.
func (e *emitter) exitDeps(tc *jobCtx, d deps) deps {
	path := tc.jp.path
	exit := map[string]bool{}
	var main []string
	for _, jp := range tc.pp.jobs {
		if jp.finally {
			exit[jp.task] = true
		} else {
			main = append(main, jp.task)
		}
	}
	var dropped []string
	d.names = slices.DeleteFunc(d.names, func(n string) bool {
		if exit[n] {
			return false
		}
		dropped = append(dropped, n)
		return true
	})
	if tasks := dependsTasks(d.depends); slices.ContainsFunc(main, func(n string) bool { return slices.Contains(tasks, n) }) {
		dropped = append(dropped, "the depends expression "+d.depends)
		d.depends = ""
		for _, jp := range tc.pp.jobs {
			if jp.finally && jp.j != tc.jp.j && slices.ContainsFunc(tc.jp.j.Needs, func(n ir.Need) bool { return n.JobID == jp.j.ID }) {
				d.names = append(d.names, jp.task)
			}
		}
	}
	if len(dropped) > 0 {
		e.lose(path, caps.JobNeeds, "an exit handler task cannot wait for a task of the entry DAG ("+strings.Join(dropped, ", ")+"); it starts after that DAG ends")
	}
	return d
}

// gateTasks writes the suspend task that holds a manual job or an approval. It returns the name of the first
// gate, which the job waits for, and puts the dependencies and the depends expression on the gate.
func (e *emitter) gateTasks(tc *jobCtx, d deps, jx map[string]any, out *jobOut) (string, deps) {
	j, path := tc.jp.j, tc.jp.path
	approval := j.Environment != nil && j.Environment.Approval
	list, _ := jx[ExtGates].([]any)
	if !j.Manual && !approval && len(list) == 0 {
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
	deps.depends = ""
	if d.depends != "" {
		deps.depends = gates[0].task + "." + atomSucceeded + " && (" + d.depends + ")"
	}
	for i, g := range gates {
		t := newMap()
		t.SetStr("name", g.task)
		t.SetStr("template", g.tmpl)
		next := deps.names
		if i+1 < len(gates) {
			next = []string{gates[i+1].task}
		}
		switch {
		case i+1 == len(gates) && d.depends != "":
			// The last gate waits as the job would, so the job never runs without the approval.
			t.SetStr("depends", d.depends)
		case len(next) > 0:
			t.Set("dependencies", strSeq(next))
		}
		out.tasks = append(out.tasks, t.N)
		tmpl := newMap()
		if body, ok := g.body.(map[string]any); ok {
			_ = tmpl.AddExtra(body, nil)
		} else {
			tmpl.SetStr("name", g.tmpl)
			tmpl.Set("suspend", newMap().N)
		}
		out.tmpls = append(out.tmpls, tmpl.N)
	}
	return gates[0].task, deps
}

// dagTask writes the DAG task of a job.
func (e *emitter) dagTask(tc *jobCtx, sx *stepCtx, cond taskCond, d deps, gate string, items *yaml.Node, whole bool, jx map[string]any, ref bool) *wmap {
	j, jp, path := tc.jp.j, tc.jp, tc.jp.path
	t := newMap()
	t.SetStr("name", jp.task)
	comments := []string{}
	if jp.task != j.ID {
		comments = append(comments, idComment(j.ID))
	}
	if j.Name != "" {
		comments = append(comments, "# "+commentSafe(j.Name))
		e.markName(path, j.Name)
	}
	if len(comments) > 0 {
		t.KeyComment("name", strings.Join(comments, "\n"))
	}
	if ref {
		t.Set("templateRef", e.node(path, "templateRef", jx["templateRef"]))
	} else {
		t.SetStr("template", jp.tmpl)
	}
	switch {
	case gate != "" && d.depends == "":
		t.Set("dependencies", strSeq([]string{gate}))
	case d.depends != "":
		t.SetStr("depends", d.depends)
	case len(d.names) > 0:
		t.Set("dependencies", strSeq(d.names))
	}
	switch {
	case cond.disabled:
		t.SetStr("when", disabledWhen())
	case cond.when != "":
		t.SetStr("when", cond.when)
	}
	if j.AllowFailure || jx["continueOn"] != nil {
		co := newMap()
		if j.AllowFailure {
			co.Set("failed", boolNode(true))
		}
		if x, ok := jx["continueOn"].(map[string]any); ok {
			_ = co.AddExtra(x, nil)
		}
		if !co.Empty() {
			t.Set("continueOn", co.N)
		}
	}
	switch {
	case items != nil && !cond.disabled:
		t.Set("withItems", items)
		if whole {
			if err := e.markArg(caps.JobMatrix, path, argOf(j.Matrix)); err != nil {
				e.lose(path, caps.JobMatrix, "the matrix marker cannot be written: "+err.Error())
			}
		}
	default:
		for _, k := range []string{"withItems", "withParam", "withSequence"} {
			if v, ok := jx[k]; ok && j.Matrix == nil {
				t.Set(k, e.node(path, k, v))
			}
		}
	}
	t.Set("arguments", e.taskArguments(tc, sx, jx, cond.disabled))
	if t.Has("arguments") && t.N.Content[len(t.N.Content)-1].Kind == yaml.MappingNode && len(t.N.Content[len(t.N.Content)-1].Content) == 0 {
		t.Drop("arguments")
	}
	rest := map[string]any{}
	for k := range taskKeys {
		if v, ok := jx[k]; ok {
			rest[k] = v
		}
	}
	_ = t.AddExtra(rest, nil)
	return t
}

// loopItem is a reference to the loop item. A task that never runs has no loop, and Argo rejects the reference.
var loopItem = regexp.MustCompile(`\{\{item(\.[^}]*)?\}\}`)

// taskArguments binds the template inputs: the loop item, the outputs of other tasks, the artifacts, and the
// arguments that an Argo file kept.
func (e *emitter) taskArguments(tc *jobCtx, sx *stepCtx, jx map[string]any, disabled bool) *yaml.Node {
	path := tc.jp.path
	args := newMap()
	var params []*yaml.Node
	bound := map[string]bool{}
	for _, p := range tc.params {
		bound[p.name] = true
		v := p.value
		if disabled {
			v = loopItem.ReplaceAllString(v, "")
		}
		params = append(params, nameValue(p.name, v))
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
		args.Set("parameters", seqOf(params...))
	}
	arts := append([]*yaml.Node{}, sx.argArts...)
	if l, ok := jx["argument_artifacts"].([]any); ok {
		written := map[string]bool{}
		for _, a := range sx.argArts {
			written[nodeName(a)] = true
		}
		for _, a := range l {
			if m, isMap := a.(map[string]any); isMap && written[strOf(m["name"])] {
				continue
			}
			arts = append(arts, e.node(path, "arguments", a))
		}
	}
	if len(arts) > 0 {
		args.Set("artifacts", seqOf(arts...))
	}
	rest := map[string]any{}
	for k, v := range jx {
		if strings.HasPrefix(k, "arguments_") {
			rest[strings.TrimPrefix(k, "arguments_")] = v
		}
	}
	_ = args.AddExtra(rest, nil)
	return args.N
}

// nodeName is the value of the name key of a mapping node.
func nodeName(n *yaml.Node) string {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "name" {
			return n.Content[i+1].Value
		}
	}
	return ""
}

// uniformDepends rewrites the dependencies of a DAG as depends text when any task of the DAG has depends, because
// Argo rejects a DAG that uses both. A plain need stays Succeeded only, as withDepends writes it.
func uniformDepends(tasks []*yaml.Node) {
	if !slices.ContainsFunc(tasks, func(n *yaml.Node) bool { return mapValue(n, "depends") != nil }) {
		return
	}
	for _, n := range tasks {
		seq := mapValue(n, "dependencies")
		if seq == nil {
			continue
		}
		var atoms []string
		for _, c := range seq.Content {
			atoms = append(atoms, c.Value+"."+atomSucceeded)
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "dependencies" {
				n.Content[i].Value = "depends"
				n.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.Join(atoms, " && ")}
			}
		}
	}
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
