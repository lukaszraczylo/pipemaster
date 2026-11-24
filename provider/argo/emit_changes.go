package argo

import (
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// changesTask names the generated DAG task and template that compute the path predicates of the job conditions.
const changesTask = "pipemaster-changes"

const (
	changesDir        = "/tmp/pipemaster-out"
	changesScriptPath = "/tmp/pipemaster-changes.sh"
)

// changesEnv maps the shell variable of the changes script to the reserved parameter that fills it.
var changesEnv = []struct{ env, param string }{
	{"EVENT", "ctx-event"}, {"REF_TYPE", "ctx-ref-type"}, {"REPO", "ctx-repo"}, {"AFTER", "ctx-sha"},
	{"HEAD", "ctx-sha"}, {"BEFORE", "ctx-before-sha"}, {"BASE", "ctx-pr-base-sha"},
}

// changesArg marks a job whose when text reads the result of the generated task; When is the text as written.
type changesArg struct {
	Src  string `json:"src"`
	When string `json:"when"`
}

func changesWhenAtom(out string) string {
	return "tasks['" + changesTask + "'].outputs.parameters." + out + " == 'true'"
}

// changesCond states a job condition with path predicates as a when that reads a result of the generated task.
func (tc *jobCtx) changesCond(c taskCond) (taskCond, bool) {
	x := tc.jp.j.If
	if tc.jp.finally || !expr.HasChanges(x) || tc.pp.byID[changesTask] != nil {
		return c, false
	}
	plan, ok := emitkit.DecomposeChanges(x)
	if !ok {
		return c, false
	}
	parts := []string{changesWhenAtom(plan.Out)}
	if plan.Residual != nil {
		for _, cj := range conjuncts(plan.Residual) {
			re, ok := tc.rewrite(cj)
			if !ok {
				return c, false
			}
			body, ok := whenBody(re)
			if !ok {
				return c, false
			}
			parts = append(parts, body)
		}
	}
	when := "{{= " + parts[0] + " }}"
	if len(parts) > 1 {
		when = "{{= (" + strings.Join(parts, ") && (") + ") }}"
	}
	if err := tc.e.markArg(caps.JobIf, tc.jp.path, changesArg{Src: expr.String(x), When: when}); err != nil {
		return c, false
	}
	tc.pp.addChanges(plan)
	tc.e.emulate(tc.jp.path, caps.JobIf, "changed-paths task "+changesTask, "rules:changes is computed by the generated task "+changesTask+" and the task runs only when its output is true: a push compares with the commit before it, a merge request with the target branch, and a tag, a new branch, a schedule or any other event counts every path as changed; the task runs in the pinned image "+emitkit.ChangesImage+" and fetches the commits from github.com, or from the host in PIPEMASTER_CHECKOUT_HOST when the task sets it, so a private repository needs credentials and an air-gapped cluster needs a mirror of the image and the repository; a commit before the push that cannot be fetched fails the task")
	c.when = when
	c.deps = append(c.deps, changesTask)
	return c, true
}

func (pp *plan) addChanges(p emitkit.ChangesPlan) {
	for _, q := range pp.changes {
		if q.Out == p.Out {
			return
		}
	}
	pp.changes = append(pp.changes, p)
	for _, c := range changesEnv {
		pp.ctx[c.param] = true
	}
}

// changesTemplate writes the generated DAG task and its template.
func (e *emitter) changesTemplate(pp *plan) (task, tmpl *yaml.Node) {
	t := newMap()
	t.SetStr("name", changesTask)
	t.SetStr("template", changesTask)
	task = t.N
	env := seqOf()
	for _, c := range changesEnv {
		env.Content = append(env.Content, nameValue(c.env, "{{workflow.parameters."+c.param+"}}"))
	}
	for _, a := range emitkit.ChangesAtomsOf(pp.changes) {
		re, _ := emitkit.AtomERE(a.Globs)
		env.Content = append(env.Content, nameValue("RE_"+a.Out, re))
	}
	body := emitkit.ChangesScript(pp.changes, true, func(out string) string { return `"` + changesDir + "/" + out + `"` })
	script := strings.Join([]string{
		"set -eu",
		"mkdir -p " + changesDir,
		"cat > " + changesScriptPath + " <<'PIPEMASTER_CHANGES_EOF'",
		strings.TrimRight(body, "\n"),
		"PIPEMASTER_CHANGES_EOF",
		"exec bash " + changesScriptPath,
	}, "\n")
	cont := newMap()
	cont.SetStr("image", emitkit.ChangesImage)
	cont.Set("command", strSeq([]string{"sh", "-c"}))
	cont.Set("args", strSeq([]string{script}))
	cont.Set("env", env)
	params := seqOf()
	for _, p := range pp.changes {
		vf := newMap()
		vf.SetStr("path", changesDir+"/"+p.Out)
		m := newMap()
		m.SetStr("name", p.Out)
		m.Set("valueFrom", vf.N)
		params.Content = append(params.Content, m.N)
	}
	outs := newMap()
	outs.Set("parameters", params)
	tm := newMap()
	tm.SetStr("name", changesTask)
	tm.Set("container", cont.N)
	tm.Set("outputs", outs.N)
	return task, tm.N
}
