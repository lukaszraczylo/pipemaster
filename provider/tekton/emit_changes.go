package tekton

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// changesTask names the generated PipelineTask that computes the path predicates of the job conditions.
const changesTask = "pipemaster-changes"

const changesScriptPath = "/tmp/pipemaster-changes.sh"

// changesParams are the reserved params the generated task reads, with the shell variable each fills.
var changesParams = []struct{ param, env string }{
	{"ctx-event", "EVENT"}, {"ctx-ref-type", "REF_TYPE"}, {"ctx-repo", "REPO"}, {"ctx-sha", "AFTER"},
	{"ctx-before-sha", "BEFORE"}, {"ctx-pr-base-sha", "BASE"},
}

// changesArg marks a job whose condition holds path predicates: Head and Back are what the generated entries read back as.
type changesArg struct {
	Src  string `json:"src"`
	Head string `json:"head"`
	Back string `json:"back,omitempty"`
}

func changesRef(out string) expr.Expr {
	return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.PrefixNeeds, changesTask, "outputs", out}}, R: expr.Lit{Kind: expr.LitStr, Str: "true"}}
}

// changesCond states a job condition with path predicates as a when entry on a result of the generated task, plus
// the entries of the rest of the condition. ok is false when the condition needs another form.
func (tc *taskCtx) changesCond(path string, x expr.Expr) (condition, bool) {
	if x == nil || tc.jp.finally || !expr.HasChanges(x) || tc.pp.byID[changesTask] != nil {
		return condition{}, false
	}
	plan, ok := emitkit.DecomposeChanges(x)
	if !ok {
		return condition{}, false
	}
	var rest []exprtekton.Entry
	if plan.Residual != nil {
		if rest, ok = tc.whenEntries(plan.Residual, pipelineScope); !ok {
			return condition{}, false
		}
	}
	head, ok := exprtekton.ToWhen(changesRef(plan.Out))
	if !ok {
		return condition{}, false
	}
	tc.pp.addChanges(plan)
	arg := changesArg{Src: expr.String(x), Head: expr.String(exprtekton.FromWhen(head))}
	if len(rest) > 0 {
		arg.Back = expr.String(exprtekton.FromWhen(rest))
	}
	if err := tc.e.markArg(caps.JobIf, path, arg); err != nil {
		return condition{}, false
	}
	tc.e.emulate(path, caps.JobIf, "changed-paths task "+changesTask, "rules:changes is computed by the generated task "+changesTask+" and the task runs only when its result is true: a push compares with the commit before it, a merge request with the target branch, and a tag, a new branch, a schedule or any other event counts every path as changed; the task installs git and bash with apk and fetches the commits from github.com, so a private repository needs credentials; a commit before the push that cannot be fetched fails the task")
	return condition{when: append(head, rest...)}, true
}

func (pp *plan) addChanges(p emitkit.ChangesPlan) {
	for _, q := range pp.changes {
		if q.Out == p.Out {
			return
		}
	}
	pp.changes = append(pp.changes, p)
	for _, c := range changesParams {
		pp.ctx[c.param] = true
	}
}

// changesTaskName is the name of the generated Task.
func (pp *plan) changesTaskName() string { return pp.name + "-" + changesTask }

// changesPipelineTask is the generated PipelineTask, and the Task document it refers to is added to the output.
func (e *emitter) changesPipelineTask(pp *plan) *yaml.Node {
	task := newMap()
	task.SetStr("apiVersion", apiPipelinesV1)
	task.SetStr("kind", "Task")
	meta := newMap()
	meta.SetStr("name", pp.changesTaskName())
	task.Set("metadata", meta.N)
	spec := newMap()
	params, results, pt := seqOf(), seqOf(), seqOf()
	env := seqOf()
	for _, c := range changesParams {
		m := newMap()
		m.SetStr("name", c.param)
		m.SetStr("default", "")
		params.Content = append(params.Content, m.N)
		pt.Content = append(pt.Content, nameValue(c.param, "$(params."+c.param+")"))
		env.Content = append(env.Content, nameValue(c.env, "$(params."+c.param+")"))
	}
	env.Content = append(env.Content, nameValue("HEAD", "$(params.ctx-sha)"))
	for _, p := range pp.changes {
		m := newMap()
		m.SetStr("name", p.Out)
		results.Content = append(results.Content, m.N)
	}
	for _, a := range emitkit.ChangesAtomsOf(pp.changes) {
		re, _ := emitkit.AtomERE(a.Globs)
		env.Content = append(env.Content, nameValue("RE_"+a.Out, re))
	}
	body := emitkit.ChangesScript(pp.changes, true, func(out string) string { return `"$(results.` + out + `.path)"` })
	script := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"if ! command -v bash >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then apk add --no-cache bash git >/dev/null; fi",
		"cat > " + changesScriptPath + " <<'PIPEMASTER_CHANGES_EOF'",
		strings.TrimRight(body, "\n"),
		"PIPEMASTER_CHANGES_EOF",
		"exec bash " + changesScriptPath,
	}, "\n")
	step := newMap()
	step.SetStr("name", "changes")
	step.SetStr("image", defaultImage)
	step.Set("env", env)
	step.Set("script", block(script))
	spec.Set("params", params)
	spec.Set("results", results)
	spec.Set("steps", seqOf(step.N))
	task.Set("spec", spec.N)
	doc := &outDoc{Root: task.N}
	e.add(tasksDir+pp.changesTaskName()+".yaml", doc)
	pipeline := newMap()
	pipeline.SetStr("name", changesTask)
	ref := newMap()
	ref.SetStr("name", pp.changesTaskName())
	pipeline.Set("taskRef", ref.N)
	pipeline.Set("params", pt)
	return pipeline.N
}
