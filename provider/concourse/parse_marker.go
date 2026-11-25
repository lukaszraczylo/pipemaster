package concourse

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// readMarks reads the pipemaster marker comments of the file. A marker holds what the emitter knew and the file
// cannot say; the parser applies it only when the file still holds the text it was written for.
func (b *pipeBuilder) readMarks(data []byte) {
	b.marks = emitkit.ParseArgs(data)
	b.bare = map[ir.FeatureUse]bool{}
	for _, u := range emitkit.ParseMarkers(data) {
		b.bare[u] = true
	}
}

// arg decodes the first argument line of the marker for a feature at a path.
func (b *pipeBuilder) arg(path string, f caps.Feature, into any) bool {
	lines := b.marks[ir.FeatureUse{IRPath: path, Feature: f}]
	return len(lines) > 0 && json.Unmarshal([]byte(lines[0]), into) == nil
}

// orphanNames reports the display-name markers whose pipeline, job or step is no longer in the file. The markers
// are keyed by the written name, so a rename in the file leaves its marker with no node to restore.
func (b *pipeBuilder) orphanNames() {
	var orphans []ir.FeatureUse
	for u := range b.marks {
		if u.Feature == caps.DisplayName && !b.present[u.IRPath] && u.IRPath != b.path() {
			orphans = append(orphans, u)
		}
	}
	slices.SortFunc(orphans, func(x, y ir.FeatureUse) int { return strings.Compare(x.IRPath, y.IRPath) })
	for _, u := range orphans {
		var a nameArg
		if !b.arg(u.IRPath, u.Feature, &a) {
			continue
		}
		b.ignored(u.IRPath, caps.DisplayName, "the "+a.Kind+" "+a.Written+" is no longer in the file under that name; the display name and the ID "+a.ID+" are lost")
	}
}

// marked is true when the file has a marker, with or without an argument, for the feature at the path.
func (b *pipeBuilder) marked(path string, f caps.Feature) bool {
	u := ir.FeatureUse{IRPath: path, Feature: f}
	return b.bare[u] || len(b.marks[u]) > 0
}

// ignored reports a marker that the file no longer matches. The file content stays as the file says.
func (b *pipeBuilder) ignored(path string, f caps.Feature, why string) {
	b.rep.Add(report.Entry{IRPath: path, Feature: f, Level: caps.Lost, Strategy: "kept as written", SourceLoc: b.file, Detail: markerIgnored + why})
}

// pipelineName restores the display name and the ID of a pipeline whose file name differs.
func (b *pipeBuilder) pipelineName(pl *ir.Pipeline) {
	var a nameArg
	if b.arg(b.path(), caps.DisplayName, &a) && a.Kind == "pipeline" {
		pl.Name = a.Name
		if a.ID != "" && a.Written == b.id {
			pl.ID = a.ID
		}
	}
}

// nameMarker puts back the name and ID of a step, which the file holds under the written name.
func (jb *jobBuilder) nameMarker(st *ir.Step) {
	var a nameArg
	if !jb.b.arg(jb.path+"/step/"+st.ID, caps.DisplayName, &a) || a.Kind != "step" || a.Written != st.ID {
		return
	}
	st.Name = a.Name
	if a.ID == "" {
		return
	}
	if jb.taken[a.ID] {
		jb.b.ignored(jb.path+"/step/"+st.ID, caps.DisplayName, "the step ID "+a.ID+" is already used by another step of the job")
		return
	}
	st.ID = a.ID
}

// applyJobNames puts back the IDs and names of the jobs that the emitter renamed, and the needs that name them.
func (b *pipeBuilder) applyJobNames(pl *ir.Pipeline) {
	ids := map[string]string{}
	for _, j := range pl.Jobs {
		var a nameArg
		if b.arg(b.path()+"/job/"+j.ID, caps.DisplayName, &a) && a.Kind == "job" && a.Written == j.ID {
			j.Name = a.Name
			if a.ID != "" {
				ids[j.ID] = a.ID
			}
		}
	}
	for _, j := range pl.Jobs {
		for i, n := range j.Needs {
			if id, ok := ids[n.JobID]; ok {
				j.Needs[i].JobID = id
			}
		}
	}
	for _, j := range pl.Jobs {
		if id, ok := ids[j.ID]; ok {
			j.ID = id
		}
	}
}

// gateMarker puts back the manual flag and the environment of a job that has no triggering get. It returns true
// when the job is held for a condition that only the carry of the job holds: the caller must then see the carry
// restored, or the job stays manual.
func (jb *jobBuilder) gateMarker(bj *builtJob) bool {
	var a gateArg
	if !jb.b.arg(jb.path, caps.JobManual, &a) {
		return false
	}
	if a.Held != bj.job.Manual {
		jb.b.ignored(jb.path, caps.JobManual, "the triggering gets of the job were edited after pipemaster wrote them")
		return false
	}
	jb.job.Manual, jb.job.Environment = a.Manual, a.Environment
	return a.Held && !a.Manual && a.Cond
}

// ifMarker moves a condition that every script task repeats back to the job.
func (jb *jobBuilder) ifMarker() {
	var a ifArg
	if !jb.b.arg(jb.path, caps.JobIf, &a) {
		return
	}
	cond, err := expr.ParseCanonical(a.If)
	if err != nil {
		jb.b.ignored(jb.path, caps.JobIf, "the condition of the marker cannot be read")
		return
	}
	var scripts []*ir.Step
	for _, st := range jb.job.Steps {
		if st.Kind == ir.StepScript {
			scripts = append(scripts, st)
		}
	}
	rest := make([]expr.Expr, len(scripts))
	for i, st := range scripts {
		x, ok := stripLead(st.If, cond)
		if !ok {
			jb.b.ignored(jb.path, caps.JobIf, "a script task no longer starts with the guard of the job condition; the guards stay in the tasks")
			return
		}
		rest[i] = x
	}
	if len(scripts) == 0 {
		jb.b.ignored(jb.path, caps.JobIf, "the job has no script task that carries the guard")
		return
	}
	for i, st := range scripts {
		st.If = rest[i]
	}
	jb.job.If = cond
}

// stripLead removes the job condition from the front of a step condition.
func stripLead(have, job expr.Expr) (expr.Expr, bool) {
	if have == nil {
		return nil, false
	}
	if expr.String(have) == expr.String(job) {
		return nil, true
	}
	if b, ok := have.(expr.Binary); ok && b.Op == expr.OpAnd && expr.String(b.L) == expr.String(job) {
		return b.R, true
	}
	return nil, false
}

// matrixMarker restores the matrix when the file still holds the across that the marker describes.
func (jb *jobBuilder) matrixMarker(steps []any) ([]any, bool) {
	var a matrixArg
	if !jb.b.arg(jb.path, caps.JobMatrix, &a) {
		return steps, false
	}
	var m *ir.Matrix
	if err := yaml.Unmarshal([]byte(a.Matrix), &m); err != nil || m == nil {
		jb.b.ignored(jb.path, caps.JobMatrix, "the matrix of the marker cannot be read")
		return steps, false
	}
	if len(steps) != 1 {
		jb.b.ignored(jb.path, caps.JobMatrix, "the plan no longer ends in one do step with an across")
		return steps, false
	}
	step, ok := asMap(steps[0])
	body, isList := []any(nil), false
	if ok {
		body, isList = step.Vals[keyDo].([]any)
	}
	if !ok || !isList || canonJSON(step.Vals["across"]) != a.Across {
		jb.b.ignored(jb.path, caps.JobMatrix, "the across was edited after pipemaster wrote it; it stays a step of the plan")
		return steps, false
	}
	for _, key := range step.Order {
		if key != keyDo && key != "across" && key != "fail_fast" {
			jb.b.ignored(jb.path, caps.JobMatrix, "the across step has the key "+key+", which the marker does not describe")
			return steps, false
		}
	}
	jb.job.Matrix = m
	return body, true
}

// allowFailureMarker reads a plan that try wraps around a do step as allow_failure.
func (jb *jobBuilder) allowFailure(plan []any) []any {
	if !jb.b.marked(jb.path, caps.JobAllowFailure) || len(plan) != 1 {
		return plan
	}
	m, ok := asMap(plan[0])
	if !ok || len(m.Order) != 1 || m.Order[0] != "try" {
		jb.b.ignored(jb.path, caps.JobAllowFailure, "the plan is no longer one try step")
		return plan
	}
	inner, ok := asMap(m.Vals["try"])
	if !ok || inner.Vals[keyDo] == nil {
		jb.b.ignored(jb.path, caps.JobAllowFailure, "the try step no longer wraps a do step")
		return plan
	}
	jb.job.AllowFailure = true
	return []any{inner}
}

// workdirMarker puts back the relative working directory that the emitter wrote under the workspace. With no
// marker, a workspace task reads its dir relative to the workspace.
func (jb *jobBuilder) workdirMarker(st *ir.Step, form bool) {
	path := jb.path + "/step/" + st.ID
	var a workdirArg
	if !jb.b.arg(path, caps.StepWorkdir, &a) {
		if form && !jb.b.marked(path, caps.StepWorkdir) {
			st.WorkDir = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(st.WorkDir, workspaceName), "/"), "./")
		}
		return
	}
	if st.WorkDir != a.Wrote {
		jb.b.ignored(path, caps.StepWorkdir, "the run.dir was edited after pipemaster wrote it")
		return
	}
	st.WorkDir = a.WorkDir
}

// secretMarker puts back the kind of the params that the marker lists.
func (jb *jobBuilder) secretMarker(st *ir.Step) {
	var a secretArg
	path := jb.path + "/step/" + st.ID
	if !jb.b.arg(path, caps.ConfigName, &a) {
		return
	}
	flip := func(names []string, secret bool) {
		for _, n := range names {
			i := slices.IndexFunc(st.Env, func(v ir.Var) bool { return v.Name == n })
			if i < 0 {
				jb.b.ignored(path, caps.ConfigName, "the param "+n+" was removed or edited after pipemaster wrote the file")
				continue
			}
			name, ok := whole(st.Env[i].Value)
			if !ok {
				jb.b.ignored(path, caps.ConfigName, "the param "+n+" is no longer one variable")
				continue
			}
			if secret {
				st.Env[i].Value = ir.Value{Kind: ir.ValueSecret, Secret: name}
			} else if identVar.MatchString(name) {
				st.Env[i].Value = ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixVars, name}}}
			}
		}
	}
	flip(a.Secrets, true)
	flip(a.Vars, false)
}

// whole is the variable name of a value that is one ((name)).
func whole(v ir.Value) (string, bool) {
	switch v.Kind {
	case ir.ValueSecret:
		return v.Secret, true
	case ir.ValueExpr:
		if r, ok := v.Expr.(expr.Ref); ok && len(r.Path) == 2 && r.Path[0] == expr.PrefixVars {
			return r.Path[1], true
		}
	}
	return "", false
}

// guardLines takes the context prelude and the guard out of the script of a task. The guard becomes the step
// condition. A prelude line goes away only when no other line reads its variable.
func (jb *jobBuilder) guardLines(st *ir.Step) {
	script := st.Script
	var pre []string
	i := 0
	for i < len(script) && preludeVar(script[i]) != "" {
		pre = append(pre, script[i])
		i++
	}
	var cond expr.Expr
	if i < len(script) {
		if c, ok := exprconcourse.ParseGuard(script[i]); ok {
			cond = c
			i++
		}
	}
	body := script[i:]
	if len(pre) == 0 && cond == nil {
		jb.disabled(st)
		return
	}
	body = refsBack(pre, body)
	st.Script = body
	if cond != nil {
		st.If = cond
	}
}

// preludeVar is the variable that a prelude line fills, or "" when the line is no prelude line.
func preludeVar(line string) string {
	rest, ok := strings.CutPrefix(line, "PM_CTX_")
	if !ok {
		return ""
	}
	name, _, ok := strings.Cut(rest, "=")
	if !ok {
		return ""
	}
	env := "PM_CTX_" + name
	ref, ok := exprconcourse.EnvRef(env)
	if !ok {
		return ""
	}
	file, ok := ctxFiles[strings.Join(ref.Path, ".")]
	if !ok {
		return ""
	}
	head := preludeLine(env, "")
	prefix := strings.TrimSuffix(head, ` ; export `+env+preludeSuffix)
	_ = prefix
	if !strings.HasPrefix(line, env+`="$(cat `) || !strings.HasSuffix(line, file+`)"; export `+env+preludeSuffix) {
		return ""
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(line, env+`="$(cat `), file+`)"; export `+env+preludeSuffix)
	if strings.Trim(mid, "./") != "workspace" && mid != workspaceName+"/" && !isDotsWorkspace(mid) {
		return ""
	}
	return env
}

// isDotsWorkspace reads "../../workspace/".
func isDotsWorkspace(s string) bool {
	rest, ok := strings.CutSuffix(s, workspaceName+"/")
	if !ok {
		return false
	}
	return strings.Trim(rest, ".") == "" && strings.Count(rest, "../")*3 == len(rest)
}

// disabled reads a task whose first line is exit 0 with the guard marker: the original lines follow it.
func (jb *jobBuilder) disabled(st *ir.Step) {
	if len(st.Script) == 0 || st.Script[0] != disabledScript && st.Script[0] != legacyDisabledScript {
		return
	}
	path := jb.path + "/step/" + st.ID
	var a disabledArg
	if !jb.b.arg(path, caps.StepIf, &a) {
		return
	}
	cond, err := expr.ParseCanonical(a.If)
	if err != nil || strings.Join(st.Script[1:], "\n") != a.Script {
		jb.b.ignored(path, caps.StepIf, "the script of the disabled step was edited after pipemaster wrote it")
		return
	}
	st.If, st.Script = cond, st.Script[1:]
}

// scheduleMarker reads a time resource that pipemaster wrote for a cron line. The cron comes back only when the
// source is still the one the marker describes.
func (b *pipeBuilder) scheduleMarker(r *resource) (sourceTrigger, bool) {
	prefix := b.path() + "/trigger/"
	for u, lines := range b.marks {
		if u.Feature != caps.TriggerSchedule || !strings.HasPrefix(u.IRPath, prefix) || len(lines) == 0 {
			continue
		}
		var a scheduleArg
		if json.Unmarshal([]byte(lines[0]), &a) != nil || a.Resource != r.name {
			continue
		}
		if canonJSON(plain(r.source())) != a.Source {
			b.ignored(u.IRPath, caps.TriggerSchedule, "the source of the time resource "+r.name+" was edited after pipemaster wrote it")
			return sourceTrigger{}, false
		}
		return sourceTrigger{trig: ir.Trigger{Kind: ir.TriggerSchedule, Cron: a.Cron, Ext: resourceExt(r.name)}}, true
	}
	return sourceTrigger{}, false
}

// heldAction restores the action step that a placeholder task keeps the place of, when the task is still the
// one pipemaster wrote.
func (jb *jobBuilder) heldAction(m *omap, written string, st *ir.Step) *ir.Step {
	path := jb.path + "/step/" + written
	var a actionArg
	if !jb.b.arg(path, caps.StepAction, &a) || a.Hold == "" {
		return st
	}
	if a.Task != written || canonJSON(plain(m)) != a.Hold {
		jb.b.ignored(path, caps.StepAction, "the placeholder task "+written+" was edited after pipemaster wrote it")
		return st
	}
	var held ir.Step
	if err := yaml.Unmarshal([]byte(a.Step), &held); err != nil {
		jb.b.ignored(path, caps.StepAction, "the action of the marker cannot be read")
		return st
	}
	return &held
}

// checkoutMarker puts back the checkout step that the source get stands for, when the get is unchanged.
func (jb *jobBuilder) checkoutMarker(gets []any) *ir.Step {
	prefix := jb.path + "/step/"
	for u, lines := range jb.b.marks {
		if u.Feature != caps.StepAction || !strings.HasPrefix(u.IRPath, prefix) || len(lines) == 0 {
			continue
		}
		var a actionArg
		if json.Unmarshal([]byte(lines[0]), &a) != nil || a.Get == "" {
			continue
		}
		var held ir.Step
		if err := yaml.Unmarshal([]byte(a.Step), &held); err != nil {
			continue
		}
		for _, g := range gets {
			if gm, ok := g.(map[string]any); ok && gm["get"] == a.Get {
				if canonJSON(gm["params"]) != a.Params {
					jb.b.ignored(u.IRPath, caps.StepAction, "the params of the get "+a.Get+" were edited after pipemaster wrote them")
					return nil
				}
				return &held
			}
		}
		jb.b.ignored(u.IRPath, caps.StepAction, "the get "+a.Get+" is no longer in the plan")
	}
	return nil
}

// placeholder is true when the jobs are the one job that pipemaster wrote for a pipeline with no jobs.
func (b *pipeBuilder) placeholder(list []any) bool {
	var a placeholderArg
	path := b.path() + "/placeholder"
	if !b.arg(path, caps.ExtPassthrough, &a) {
		return false
	}
	if len(list) != 1 || canonJSON(plain(list[0])) != a.Hold {
		b.ignored(path, caps.ExtPassthrough, "the placeholder job was edited after pipemaster wrote it")
		return false
	}
	return true
}

// carryOf reads the carry marker at a path when the digest of the file text still matches.
func (b *pipeBuilder) carryOf(path, digest string, into any) bool {
	var a carryArg
	if !b.arg(path+carrySuffix, caps.ExtPassthrough, &a) {
		return false
	}
	if a.Digest != digest {
		why := "the text beside the carry of " + path + " was edited after pipemaster wrote it"
		if yaml.Unmarshal([]byte(a.Carry), into) == nil {
			if held := carryFields(into); held != "" {
				why += "; not restored: " + held
			}
		}
		b.ignored(path+carrySuffix, caps.ExtPassthrough, why)
		return false
	}
	if err := yaml.Unmarshal([]byte(a.Carry), into); err != nil {
		b.ignored(path+carrySuffix, caps.ExtPassthrough, "the carry of "+path+" cannot be read")
		return false
	}
	return true
}

// carryStep puts back the step fields that the task cannot say: the image, the shell, the env, the condition.
func (jb *jobBuilder) carryStep(m *omap, written string, st *ir.Step) {
	var c ir.Step
	if !jb.b.carryOf(jb.path+"/step/"+written, digestOf(plainValue(m)), &c) {
		return
	}
	script := st.Script
	if len(c.Script) > 0 {
		script = c.Script
	}
	c.Script, c.Ext = script, st.Ext
	if _, bare := statusOf(c.If); bare {
		c.If = st.If
	}
	if c.ContinueOnError {
		if jb.tried == nil {
			jb.tried = map[*ir.Step]string{}
		}
		jb.tried[st] = written
	}
	c.ContinueOnError = false
	id := st.ID
	*st = c
	if st.ID != id && jb.taken[st.ID] {
		st.ID = id
		jb.b.ignored(jb.path+"/step/"+written, caps.ExtPassthrough, "the carry ID "+c.ID+" is already used by another step of the job")
	}
}

// untried reports the steps whose carry held continue_on_error and that no try wraps any more.
func (jb *jobBuilder) untried() {
	for _, st := range jb.job.Steps {
		if name, ok := jb.tried[st]; ok && !st.ContinueOnError {
			jb.b.ignored(jb.path+"/step/"+name, caps.StepContinueOnError, "the carry holds continue_on_error true, but no try wraps the task "+name+" in the file")
		}
	}
}

// carryJob puts back the job fields that the plan cannot say.
func (jb *jobBuilder) carryJob(m *omap) bool {
	var c ir.Job
	if !jb.b.carryOf(jb.path, digestOf(plainValue(m)), &c) {
		return false
	}
	j := jb.job
	needs := j.Needs
	if len(needs) == len(c.Needs) {
		for i := range needs {
			needs[i].Artifacts, needs[i].Optional = c.Needs[i].Artifacts, c.Needs[i].Optional
		}
	}
	c.ID, c.Name, c.Steps, c.Ext, c.Needs = j.ID, j.Name, j.Steps, j.Ext, needs
	*j = c
	return true
}

// carryPipeline puts back the pipeline fields that the resources and gets cannot say.
func (b *pipeBuilder) carryPipeline(top *omap, list []any, pl *ir.Pipeline) bool {
	var res any
	if v := top.Vals["resources"]; v != nil {
		res = plainValue(v)
	}
	var gets []any
	for _, raw := range list {
		m, ok := asMap(raw)
		if !ok {
			continue
		}
		steps, _ := m.Vals[keyPlan].([]any)
		for _, s := range steps {
			if sm, ok := asMap(s); ok && sm.Vals["get"] != nil {
				gets = append(gets, plainValue(sm))
			}
		}
	}
	var c ir.Pipeline
	if !b.carryOf(b.path(), digestOf([]any{res, gets}), &c) {
		return false
	}
	c.ID, c.Jobs, c.Ext = pl.ID, pl.Jobs, pl.Ext
	*pl = c
	return true
}

// refsBack drops the context prelude lines and writes each ${PM_CTX_*} use in the body as the reference it stands for.
func refsBack(pre, body []string) []string {
	out := slices.Clone(body)
	for _, l := range pre {
		env := preludeVar(l)
		ref, ok := exprconcourse.EnvRef(env)
		if !ok {
			continue
		}
		tmpl, err := expr.PrintScriptTemplate([]expr.Part{{Expr: ref}})
		if err != nil {
			continue
		}
		for i := range out {
			out[i] = strings.ReplaceAll(out[i], "${"+env+"}", tmpl)
		}
	}
	return out
}

// matrixBack writes each ${PM_MATRIX_*} use of a task as the matrix reference its param stands for, and drops the
// param. It runs only for a job whose matrix came back from its marker.
func (jb *jobBuilder) matrixBack(st *ir.Step) {
	if jb.job.Matrix == nil {
		return
	}
	cx, _ := st.Ext[dialect].(map[string]any)
	params, _ := cx[ExtParams].(map[string]any)
	for name, v := range params {
		raw, _ := v.(string)
		key, ok := matrixKey(raw)
		if !ok || !strings.HasPrefix(name, "PM_MATRIX_") {
			continue
		}
		ref := expr.Ref{Path: []string{expr.PrefixMatrix, key}}
		tmpl, err := expr.PrintScriptTemplate([]expr.Part{{Expr: ref}})
		if err != nil {
			continue
		}
		for i := range st.Script {
			st.Script[i] = strings.ReplaceAll(st.Script[i], "${"+name+"}", tmpl)
		}
		delete(params, name)
	}
	if len(params) == 0 && cx != nil {
		delete(cx, ExtParams)
		if len(cx) == 0 {
			delete(st.Ext, dialect)
			if len(st.Ext) == 0 {
				st.Ext = nil
			}
		}
	}
}

// matrixKey reads ((.:key)) and ((.:matrix.key)), the local var of a matrix cell.
func matrixKey(v string) (string, bool) {
	rest, ok := strings.CutPrefix(v, "((.:")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutSuffix(rest, "))")
	if !ok || rest == "" {
		return "", false
	}
	rest = strings.TrimPrefix(rest, cellVar+".")
	return rest, true
}

// carryFields names the IR fields that a carry held, for the entry of a carry that was dropped.
func carryFields(v any) string {
	var out []string
	names := func(env []ir.Var) string {
		n := make([]string, len(env))
		for i, e := range env {
			n[i] = e.Name
		}
		return strings.Join(n, ", ")
	}
	switch c := v.(type) {
	case *ir.Step:
		if c.Image != nil {
			out = append(out, "step image "+c.Image.Ref)
		}
		if c.Shell != "" {
			out = append(out, "step shell "+c.Shell)
		}
		if len(c.Env) > 0 {
			out = append(out, "step env "+names(c.Env))
		}
		if c.WorkDir != "" {
			out = append(out, "step workdir "+c.WorkDir)
		}
		if c.If != nil {
			out = append(out, "step condition "+expr.String(c.If))
		}
		if len(c.Script) > 0 {
			out = append(out, "step script")
		}
	case *ir.Job:
		out = jobFields(c, names)
	case *ir.Pipeline:
		if len(c.Env) > 0 {
			out = append(out, "pipeline env "+names(c.Env))
		}
		if len(c.Triggers) > 0 {
			out = append(out, fmt.Sprintf("%d pipeline triggers", len(c.Triggers)))
		}
		if c.Concurrency != nil {
			out = append(out, "pipeline concurrency")
		}
		if c.Permissions != nil {
			out = append(out, "pipeline permissions")
		}
		if len(c.Inputs) > 0 {
			out = append(out, "pipeline inputs")
		}
		if len(c.Finally) > 0 {
			out = append(out, "finally jobs")
		}
	}
	return strings.Join(out, ", ")
}

func jobFields(c *ir.Job, names func([]ir.Var) string) []string {
	var out []string
	if len(c.Runner.Labels) > 0 {
		out = append(out, "runs-on labels "+fmt.Sprint(c.Runner.Labels))
	}
	if len(c.Runner.Selector) > 0 || c.Runner.Platform != "" {
		out = append(out, "runner selector or platform")
	}
	if c.Container != nil {
		out = append(out, "job image "+c.Container.Ref)
	}
	if len(c.Env) > 0 {
		out = append(out, "job env "+names(c.Env))
	}
	if len(c.Cache) > 0 {
		out = append(out, "job cache")
	}
	if len(c.Artifacts) > 0 || len(c.Outputs) > 0 {
		out = append(out, "job artifacts")
	}
	if len(c.Services) > 0 {
		out = append(out, "job services")
	}
	if c.Retry != nil && len(c.Retry.On) > 0 {
		out = append(out, "retry conditions "+strings.Join(c.Retry.On, ", "))
	}
	if c.Environment != nil {
		out = append(out, "environment "+c.Environment.Name)
	}
	if c.If != nil {
		out = append(out, "job condition "+expr.String(c.If))
	}
	for _, n := range c.Needs {
		if n.Artifacts || n.Optional {
			out = append(out, "need flags of "+n.JobID)
		}
	}
	return out
}

// finallyHooks takes the finally jobs that the carry restored out of the hooks that stood for them. When the
// hooks were edited, the file wins: the finally jobs are not restored and the hook steps stay.
func (b *pipeBuilder) finallyHooks(pl *ir.Pipeline, built []*builtJob) {
	if len(pl.Finally) == 0 {
		return
	}
	var a finallyArg
	if !b.arg(b.path(), caps.PipelineFinally, &a) {
		return
	}
	var sink *builtJob
	for _, bj := range built {
		if bj.name == a.Job {
			sink = bj
		}
	}
	taskSet := map[string]bool{}
	for _, t := range a.Tasks {
		taskSet[t] = true
	}
	found := 0
	if sink != nil {
		for _, st := range sink.job.Steps {
			if taskSet[sink.written[st]] {
				found++
			}
		}
	}
	if sink == nil || sink.hookDigest != a.Digest || found != len(a.Tasks) {
		pl.Finally = nil
		b.ignored(b.path(), caps.PipelineFinally, "the hooks of the job "+a.Job+" were edited after pipemaster wrote them; the finally jobs are not restored and the hook steps stay")
		return
	}
	sink.job.Steps = slices.DeleteFunc(sink.job.Steps, func(st *ir.Step) bool { return taskSet[sink.written[st]] })
}
