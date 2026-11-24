package tekton

import (
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	kindEventListenerName = "pipemaster"
	// scheduleListenerName is the EventListener of the schedule entries. It has no GitHub signature check, so the
	// webhook listener, which GitHub must reach from outside the cluster, does not hold them.
	scheduleListenerName = kindEventListenerName + "-schedule"
	pipelineKey          = "Pipeline"
)

// project writes every pipeline, then the triggers and the kept resources.
func (e *emitter) project(pls []*ir.Pipeline) error {
	e.planNames(pls)
	for _, pp := range e.plans {
		if err := e.pipeline(pp); err != nil {
			return err
		}
	}
	e.resources(pls)
	return e.triggers()
}

// pipeline writes the Pipeline document and the Task of every job.
func (e *emitter) pipeline(pp *plan) error {
	pl := pp.pl
	e.foreignExt(pp.path, pl.Ext)
	root := newMap()
	root.SetStr("apiVersion", apiPipelinesV1)
	x := map[string]any{}
	if e.own {
		x = extOf(pl.Ext)
	}
	// A pipeline that came from a PipelineRun with an embedded spec goes back into one, with the run keys.
	run, isRun := x["pipelineRun"].(map[string]any)
	root.SetStr("kind", map[bool]string{false: pipelineKey, true: kindPipelineRun}[isRun])
	meta := newMap()
	md, hasMeta := x["metadata"].(map[string]any)
	if _, generated := md["generateName"]; !isRun || !generated {
		meta.SetStr("name", pp.name)
	}
	if hasMeta {
		if err := meta.AddExtra(md, nil); err != nil {
			return err
		}
	}
	if pp.name != pl.ID && meta.Has("name") {
		meta.KeyComment("name", idComment(pl.ID))
	}
	root.Set("metadata", meta.N)
	d := &outDoc{Root: root.N}
	pp.doc = d
	e.add(pipelineFile, d, pp.path)
	if pl.Concurrency != nil || pl.Permissions != nil {
		// Decide already reported both as lost.
		e.comment(pp.path, "pipeline concurrency and permissions have no Tekton form")
	}
	if len(pl.Env) > 0 {
		e.emulate(pp.path, caps.PipelineEnv, "stepTemplate env", "the pipeline env is copied into the stepTemplate of every Task")
		body, err := carryYAML(pl.Env)
		if err != nil {
			return err
		}
		if err := e.markArg(caps.PipelineEnv, pp.path, envArg{Env: body}); err != nil {
			return err
		}
	}
	if len(pp.renamed) > 0 {
		if err := e.markArg(caps.PipelineInputs, pp.path, inputsArg{Names: pp.renamed}); err != nil {
			return err
		}
	}
	var tasks, finally []*yaml.Node
	for _, jp := range pp.jobs {
		pt, err := e.job(pp, jp)
		if err != nil {
			return err
		}
		if jp.finally {
			finally = append(finally, pt.N)
		} else {
			tasks = append(tasks, pt.N)
		}
	}
	if len(pp.changes) > 0 {
		tasks = append([]*yaml.Node{e.changesPipelineTask(pp)}, tasks...)
	}
	spec := newMap()
	if v, ok := x["description"]; ok {
		spec.Set("description", e.node(pp.path, "description", v))
	}
	if pl.Name != "" {
		spec.SetStr("displayName", pl.Name)
	}
	spec.Set("params", e.pipelineParams(pp, x))
	if pp.ws || x["workspaces"] != nil {
		spec.Set("workspaces", e.pipelineWorkspaces(pp, x))
	}
	if v, ok := x["results"]; ok {
		spec.Set("results", e.node(pp.path, "results", v))
	}
	spec.Set("tasks", seqOf(tasks...))
	if len(finally) > 0 {
		spec.Set("finally", seqOf(finally...))
	}
	if isRun {
		runSpec := newMap()
		runSpec.Set("pipelineSpec", spec.N)
		if err := runSpec.AddExtra(run, nil); err != nil {
			return err
		}
		spec = runSpec
	}
	root.Set("spec", spec.N)
	e.loseUnknownExt(pp.path, x, map[string]bool{"metadata": true, "description": true, "workspaces": true, "results": true, "params": true, "pipelineRun": true, ExtResources: true})
	return nil
}

// loseUnknownExt reports the Tekton Ext keys of a node that the emitter does not write.
func (e *emitter) loseUnknownExt(path string, x map[string]any, known map[string]bool) {
	if !e.own {
		return
	}
	for _, k := range sortedKeys(x) {
		if !known[k] {
			e.lose(path, caps.ExtPassthrough, "Ext key "+k+" has no place in the emitted Tekton files")
		}
	}
}

// paramTypes are the param types that Tekton knows.
var paramTypes = []string{"string", "array", "object"}

// paramType gives the Tekton type of an input. A type with no Tekton form is written as string, which the
// metadata restores.
func (e *emitter) paramType(path string, in ir.Input) string {
	if slices.Contains(paramTypes, in.Type) {
		return in.Type
	}
	e.lose(path, caps.PipelineInputs, "input "+in.Name+": type "+in.Type+" has no Tekton param type; it is written as string")
	return "string"
}

// pipelineParams lists the input params, then the ctx-* params that a task reads.
func (e *emitter) pipelineParams(pp *plan, x map[string]any) *yaml.Node {
	extra, _ := x["params"].(map[string]any)
	out := seqOf()
	for _, in := range pp.pl.Inputs {
		m := newMap()
		m.SetStr("name", pp.inputs[in.Name])
		if in.Type != "" {
			m.SetStr("type", e.paramType(pp.path, in))
		}
		if in.Description != "" {
			m.SetStr("description", in.Description)
		}
		sub, kept := extra[in.Name].(map[string]any)
		// A default that Ext holds keeps the type it had in the Tekton file.
		if _, structured := sub["default"]; !structured && (!in.Required || in.Default != "") {
			switch in.Type {
			case "array":
				m.Set("default", seqOf())
			case "object":
				m.Set("default", newMap().N)
			default:
				m.SetStr("default", in.Default)
			}
		}
		if in.Required && in.Default != "" {
			e.lose(pp.path, caps.PipelineInputs, "input "+in.Name+": a Tekton param with a default is optional, so required is lost")
		}
		if kept {
			_ = m.AddExtra(sub, nil)
		}
		out.Content = append(out.Content, m.N)
	}
	for _, p := range ctxParams() {
		if pp.ctx[p] {
			m := newMap()
			m.SetStr("name", p)
			m.SetStr("default", "")
			out.Content = append(out.Content, m.N)
		}
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}

// pipelineWorkspaces declares the shared workspace and the workspaces kept from a Tekton file.
func (e *emitter) pipelineWorkspaces(pp *plan, x map[string]any) *yaml.Node {
	out := seqOf()
	if raw, ok := x["workspaces"].([]any); ok {
		for _, r := range raw {
			if n := e.node(pp.path, "workspaces", r); n != nil {
				out.Content = append(out.Content, n)
			}
		}
	}
	if pp.ws {
		m := newMap()
		m.SetStr("name", workspaceName)
		out.Content = append(out.Content, m.N)
	}
	return out
}

// pipelineTask writes the PipelineTask of a job.
func (tc *taskCtx) pipelineTask(jc condition) (*wmap, error) {
	j := tc.jp.j
	pt := newMap()
	pt.SetStr("name", tc.jp.tname)
	if tc.jp.tname != j.ID {
		pt.KeyComment("name", idComment(j.ID))
	}
	if j.Name != "" {
		pt.SetStr("displayName", j.Name)
	}
	ref := newMap()
	ref.SetStr("name", tc.jp.kname)
	pt.Set("taskRef", ref.N)
	params := seqOf()
	for _, p := range tc.params {
		if v, ok := tc.value[p]; ok {
			params.Content = append(params.Content, nameValue(p, v))
		}
	}
	tc.extraParams(params)
	if len(params.Content) > 0 {
		pt.Set("params", params)
	}
	tc.e.runAfter(tc.pp, tc.jp, pt, tc.needed)
	if err := tc.pipelineTaskKeys(pt, jc); err != nil {
		return nil, err
	}
	return pt, nil
}

// pipelineTaskKeys writes the keys of a PipelineTask that no reference or param decides: the condition, matrix,
// retries, timeout, error policy, workspace bindings and the kept keys.
func (tc *taskCtx) pipelineTaskKeys(pt *wmap, jc condition) error {
	j := tc.jp.j
	if len(jc.when) > 0 {
		pt.Set("when", whenNode(jc.when))
	}
	if err := tc.matrixNode(pt); err != nil {
		return err
	}
	if j.Retry != nil {
		if len(j.Retry.On) > 0 {
			tc.e.lose(tc.jp.path, caps.JobRetry, "retry conditions "+join(j.Retry.On)+" have no Tekton form; every failure retries")
		}
		if j.Retry.Attempts > 1 {
			pt.Set("retries", intNode(j.Retry.Attempts-1))
		}
	}
	if j.Timeout != nil {
		pt.SetStr("timeout", time.Duration(*j.Timeout).String())
	}
	if j.AllowFailure {
		pt.SetStr("onError", onErrorContinue)
	}
	px, _ := tc.jobExt()
	x := extOf(j.Ext)
	if !tc.e.own {
		x = nil
	}
	pt.Set("workspaces", tc.pipelineWorkspaceBindings(x))
	if v, ok := x["description"]; ok {
		pt.Set("description", tc.e.node(tc.jp.path, "description", v))
	}
	for _, k := range sortedKeys(px) {
		pt.Set(k, tc.e.node(tc.jp.path, "key "+k, px[k]))
	}
	return nil
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// runAfter writes the ordering edges of a job. A finally task has no ordering among finally tasks, so such an
// edge is lost. An edge that a result reference already implies is left out when the Tekton file had it so.
func (e *emitter) runAfter(pp *plan, jp *jobPlan, pt *wmap, needed map[string]bool) {
	var after []string
	var held []ir.Need
	implicit := map[string]bool{}
	if e.own {
		if l, ok := extOf(jp.j.Ext)["implicit_needs"].([]any); ok {
			for _, v := range l {
				if s, ok := v.(string); ok {
					implicit[s] = true
				}
			}
		}
	}
	written := ""
	if len(implicit) > 0 {
		if b, err := yaml.Marshal(pt.N); err == nil {
			written = string(b)
		}
	}
	for _, n := range jp.j.Needs {
		nj, ok := pp.byID[n.JobID]
		if n.Optional {
			e.lose(jp.path, caps.JobNeeds, "Tekton has no optional dependency; the need on "+n.JobID+" is required")
		}
		switch {
		case !ok:
			e.lose(jp.path, caps.JobNeeds, "need "+n.JobID+" names no job")
		case jp.finally && nj.finally:
			e.lose(jp.path, caps.JobNeeds, "the finally task cannot wait for the finally task "+n.JobID+"; Tekton runs finally tasks together")
		case jp.finally:
			held = append(held, n)
		case nj.finally:
			e.lose(jp.path, caps.JobNeeds, "a task cannot wait for the finally task "+n.JobID)
		case implicit[n.JobID] && (needed[n.JobID] || strings.Contains(written, "$(tasks."+nj.tname+".")):
		default:
			after = append(after, nj.tname)
		}
	}
	if len(after) > 0 {
		pt.Set("runAfter", strSeq(after))
	}
	if len(held) > 0 {
		e.emulate(jp.path, caps.JobNeeds, "implied by finally", "a finally task runs after every other task, so the needs are not written; the marker holds them")
		if err := e.markArg(caps.JobNeeds, jp.path, needsArg{Needs: held}); err != nil {
			e.lose(jp.path, caps.JobNeeds, err.Error())
		}
	}
}

// matrixNode writes the job matrix. Exclude entries are removed by listing the remaining combinations.
func (tc *taskCtx) matrixNode(pt *wmap) error {
	mx := tc.jp.j.Matrix
	if mx == nil {
		return nil
	}
	if mx.FailFast != nil || mx.MaxParallel > 0 {
		tc.e.lose(tc.jp.path, caps.JobMatrix, "fail-fast and max-parallel have no Tekton form")
	}
	m := newMap()
	names, _ := extOf(tc.jp.j.Ext)["matrix_include_names"].([]any)
	var include []*yaml.Node
	if len(mx.Exclude) > 0 && len(mx.Axes) > 0 {
		tc.e.emulate(tc.jp.path, caps.JobMatrix, "expanded combinations", "exclude entries are applied by writing the remaining combinations as include entries")
		if err := tc.e.markArg(caps.JobMatrix, tc.jp.path, matrixArg{Axes: mx.Axes, Exclude: mx.Exclude, Include: mx.Include}); err != nil {
			return err
		}
		for _, c := range expandAxes(mx.Axes) {
			if !excluded(c, mx.Exclude) {
				include = append(include, includeNode("", c))
			}
		}
	} else if len(mx.Axes) > 0 {
		ps := seqOf()
		for _, a := range mx.Axes {
			p := newMap()
			p.SetStr("name", a.Name)
			p.Set("value", strSeq(a.Values))
			ps.Content = append(ps.Content, p.N)
		}
		m.Set("params", ps)
	}
	for i, c := range mx.Include {
		name := ""
		if tc.e.own && i < len(names) {
			name, _ = names[i].(string)
		}
		include = append(include, includeNode(name, c))
	}
	if len(include) > 0 {
		m.Set("include", seqOf(include...))
	}
	pt.Set("matrix", m.N)
	return nil
}

func includeNode(name string, c ir.Combo) *yaml.Node {
	m := newMap()
	if name != "" {
		m.SetStr("name", name)
	}
	ps := seqOf()
	for _, kv := range c {
		ps.Content = append(ps.Content, nameValue(kv.Key, kv.Value))
	}
	m.Set("params", ps)
	return m.N
}

// expandAxes lists the combinations of the axes in order.
func expandAxes(axes []ir.Axis) []ir.Combo {
	combos := []ir.Combo{nil}
	for _, a := range axes {
		var next []ir.Combo
		for _, c := range combos {
			for _, v := range a.Values {
				next = append(next, append(append(ir.Combo{}, c...), ir.KV{Key: a.Name, Value: v}))
			}
		}
		combos = next
	}
	return combos
}

// excluded reports whether a combination matches an exclude entry: every key of the entry has the same value.
func excluded(c ir.Combo, ex []ir.Combo) bool {
	have := map[string]string{}
	for _, kv := range c {
		have[kv.Key] = kv.Value
	}
	for _, e := range ex {
		match := len(e) > 0
		for _, kv := range e {
			if v, ok := have[kv.Key]; !ok || v != kv.Value {
				match = false
			}
		}
		if match {
			return true
		}
	}
	return false
}

// resources writes the documents that a Tekton file kept whole, so they come back unchanged.
func (e *emitter) resources(pls []*ir.Pipeline) {
	if !e.own {
		return
	}
	var docs []resourceDoc
	seen := map[string]bool{}
	for _, pp := range e.plans {
		raw, _ := extOf(pp.pl.Ext)["resources"].([]any)
		for _, r := range raw {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := m["kind"].(string)
			meta, _ := m["metadata"].(map[string]any)
			name, _ := meta["name"].(string)
			key := kind + " " + name
			if seen[key] {
				continue
			}
			seen[key] = true
			docs = append(docs, resourceDoc{kind: kind, name: name, body: m, path: pp.path})
		}
	}
	for _, d := range docs {
		if d.kind == kindEventListener {
			if e.pendingEL == nil {
				e.pendingEL = map[string][]resourceDoc{}
			}
			e.pendingEL[d.name] = append(e.pendingEL[d.name], d)
			continue
		}
		e.writeResource(d)
	}
	_ = pls
}

// writeResource writes a document that a Tekton file kept whole.
func (e *emitter) writeResource(d resourceDoc) {
	n, err := plainNode(d.body)
	if err != nil {
		e.lose(d.path, caps.ExtPassthrough, "resource "+d.kind+" "+d.name+" cannot be written: "+err.Error())
		return
	}
	file := resourcesDir + sanitizeFile(d.kind+"-"+d.name) + ".yaml"
	e.made[d.kind+" "+d.name] = true
	e.add(file, &outDoc{Root: n}, d.path+"#resource/"+d.kind+"/"+d.name)
}

type resourceDoc struct {
	body map[string]any
	kind string
	name string
	path string
}

func sanitizeFile(s string) string { return unsafeName.ReplaceAllString(s, "_") }
