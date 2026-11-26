package gha

import (
	"path"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/inline"
	"github.com/lukaszraczylo/pipemaster/report"
)

// workflowDirs are the directories that a workflow file of the GHA family can live in.
var workflowDirs = []string{".github/workflows", ".gitea/workflows", ".forgejo/workflows"}

const (
	extEventKey = "event"
	eventCall   = "workflow_call"
)

// callResolver attaches the called workflow to each job-level uses: of a local workflow file.
type callResolver struct {
	byFile  map[string]*ir.Pipeline
	byStem  map[string]*ir.Pipeline
	file    map[*ir.Pipeline]string
	done    map[*ir.Pipeline]bool
	used    map[*ir.Pipeline]bool
	rep     *report.Report
	dialect string
	extra   []report.Entry
}

// attachCalls reads each local reusable workflow that a job calls as the child of that job. A workflow that only
// workflow_call starts has no other life, so it leaves the project's pipelines; one with other triggers stays.
// A call whose file is not among the workflow files keeps its fail-loud form.
func attachCalls(proj *ir.Project, rep *report.Report, dialect string) {
	if proj == nil {
		return
	}
	r := &callResolver{
		byFile: map[string]*ir.Pipeline{}, byStem: map[string]*ir.Pipeline{}, file: map[*ir.Pipeline]string{}, done: map[*ir.Pipeline]bool{},
		used: map[*ir.Pipeline]bool{}, rep: rep, dialect: dialect,
	}
	for _, f := range proj.Origin.Files {
		stem := strings.TrimSuffix(path.Base(f), path.Ext(f))
		for _, pl := range proj.Pipelines {
			if pl != nil && pl.ID == stem {
				r.byFile[f] = pl
				r.byStem[stem] = pl
				r.file[pl] = f
			}
		}
	}
	for _, pl := range proj.Pipelines {
		r.resolve(pl, nil)
	}
	proj.Pipelines = slices.DeleteFunc(proj.Pipelines, func(pl *ir.Pipeline) bool { return r.used[pl] && callOnly(pl) })
	rep.Entries = slices.DeleteFunc(rep.Entries, func(e report.Entry) bool {
		for pl := range r.used {
			if callOnly(pl) && (e.IRPath == "pipeline/"+pl.ID || strings.HasPrefix(e.IRPath, "pipeline/"+pl.ID+"/")) {
				return true
			}
		}
		return false
	})
	rep.Entries = append(rep.Entries, r.extra...)
	ir.Normalize(proj)
}

func (r *callResolver) resolve(pl *ir.Pipeline, stack []*ir.Pipeline) {
	if pl == nil || r.done[pl] || slices.Contains(stack, pl) {
		return
	}
	stack = append(stack, pl)
	for _, j := range pl.Jobs {
		st := localCall(j)
		if st == nil {
			continue
		}
		file := path.Clean(strings.TrimPrefix(st.Action.Path, "./"))
		target := r.byFile[file]
		if target == nil && slices.Contains(workflowDirs, path.Dir(file)) {
			// Gitea and Forgejo read the workflows of every family directory, so a path into another one names the same file.
			target = r.byStem[strings.TrimSuffix(path.Base(file), path.Ext(file))]
		}
		if target == nil || slices.Contains(stack, target) {
			continue
		}
		r.resolve(target, stack)
		cp := ir.Clone(&ir.Project{Pipelines: []*ir.Pipeline{target}}).Pipelines[0]
		j.Child = &ir.Child{Pipeline: cp, Native: nativeCalls(r.dialect), Files: []string{r.file[target]}}
		r.used[target] = true
		var own []report.Entry
		for _, e := range r.rep.Entries {
			if e.IRPath == "pipeline/"+target.ID || strings.HasPrefix(e.IRPath, "pipeline/"+target.ID+"/") {
				own = append(own, e)
			}
		}
		r.extra = append(r.extra, inline.RemapPaths(own, target.ID, pl.ID, j.ID)...)
	}
	r.done[pl] = true
}

// nativeCalls lists the targets that write the child as the job-level uses: it came from: the source dialect alone,
// so a file returns as the user wrote it. No other target takes the call natively, Gitea and Forgejo included, because the
// converter claims no support it cannot cite. GitHub documents local calls
// (https://docs.github.com/en/actions/reference/workflows-and-actions/reusing-workflow-configurations: reusable workflows
// "in the same repository as the calling repository can be referenced with just the path and filename"), and so does
// Forgejo (https://forgejo.org/docs/latest/user/actions/reference/, jobs.<job_id>.uses: "./.forgejo/workflows/reusable.yml
// - refers to a workflow file within the same repository as the current workflow is defined"). The Gitea docs
// (https://docs.gitea.com/usage/actions/comparison) do not mention them, so a Gitea file only keeps its own call on a return to Gitea.
func nativeCalls(dialect string) []string { return []string{dialect} }

// localCall is the call step of a job whose uses: names a local file.
func localCall(j *ir.Job) *ir.Step {
	if j == nil || len(j.Steps) != 1 || j.Steps[0] == nil {
		return nil
	}
	st := j.Steps[0]
	if st.Kind != ir.StepPipelineCall || st.Action == nil || st.Action.Kind != ir.ActionLocal {
		return nil
	}
	return st
}

// callOnly is true for a pipeline that no event starts except a workflow call.
func callOnly(pl *ir.Pipeline) bool {
	if len(pl.Triggers) == 0 {
		return false
	}
	for _, t := range pl.Triggers {
		if t.Kind != ir.TriggerAPI {
			return false
		}
		ok := false
		for _, e := range t.Ext {
			m, _ := e.(map[string]any)
			if ev, _ := m[extEventKey].(string); ev == eventCall {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// withNativeChildren adds the called workflows of a project as pipelines, so the emitter writes the file that
// each job-level uses: names. A workflow that is already a pipeline of the project, because other events start it
// too, is not added twice.
func withNativeChildren(p *ir.Project, target string) *ir.Project {
	if p == nil {
		return p
	}
	have := map[string]bool{}
	for _, pl := range p.Pipelines {
		if pl != nil {
			have[pl.ID] = true
		}
	}
	var add []*ir.Pipeline
	queue := slices.Clone(p.Pipelines)
	for len(queue) > 0 {
		pl := queue[0]
		queue = queue[1:]
		if pl == nil {
			continue
		}
		for _, j := range pl.Jobs {
			if j == nil || !inline.Native(j.Child, target) || j.Child.Pipeline == nil || have[j.Child.Pipeline.ID] {
				continue
			}
			have[j.Child.Pipeline.ID] = true
			add = append(add, j.Child.Pipeline)
			queue = append(queue, j.Child.Pipeline)
		}
	}
	if len(add) == 0 {
		return p
	}
	cp := *p
	cp.Pipelines = append(slices.Clone(p.Pipelines), add...)
	return &cp
}
