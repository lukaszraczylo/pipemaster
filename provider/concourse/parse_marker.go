package concourse

import (
	"encoding/json"
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
	if a.ID != "" {
		st.ID = a.ID
	}
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

// gateMarker puts back the manual flag and the environment of a job that has no triggering get.
func (jb *jobBuilder) gateMarker(bj *builtJob) {
	var a gateArg
	if !jb.b.arg(jb.path, caps.JobManual, &a) {
		return
	}
	gated := a.Manual || (a.Environment != nil && a.Environment.Approval)
	if gated != bj.job.Manual {
		jb.b.ignored(jb.path, caps.JobManual, "the triggering gets of the job were edited after pipemaster wrote them")
		return
	}
	jb.job.Manual, jb.job.Environment = a.Manual, a.Environment
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

// workdirMarker puts back the relative working directory that the emitter wrote under the workspace.
func (jb *jobBuilder) workdirMarker(st *ir.Step) {
	var a workdirArg
	if !jb.b.arg(jb.path+"/step/"+st.ID, caps.StepWorkdir, &a) {
		return
	}
	if st.WorkDir != a.Wrote {
		jb.b.ignored(jb.path+"/step/"+st.ID, caps.StepWorkdir, "the run.dir was edited after pipemaster wrote it")
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
	var kept []string
	for _, l := range pre {
		v := preludeVar(l)
		if cond != nil && !slices.ContainsFunc(body, func(b string) bool { return strings.Contains(b, v) }) {
			continue
		}
		if cond == nil && !slices.ContainsFunc(body, func(b string) bool { return strings.Contains(b, v) }) {
			continue
		}
		kept = append(kept, l)
	}
	st.Script = append(kept, body...)
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
	if len(st.Script) == 0 || st.Script[0] != disabledScript {
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
