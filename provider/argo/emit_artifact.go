package argo

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

var globChars = regexp.MustCompile(`[*?\[]`)

// textOrLose converts template text for a value field; ok is false when it has no Argo spelling.
func (tc *jobCtx) textOrLose(path string, f caps.Feature, what, s string) (string, bool) {
	parts, err := expr.ParseScriptTemplate(s)
	if err != nil {
		parts = []expr.Part{{Text: s}}
	}
	out, lost, hazards := tc.convert(newScope(), valueText, parts)
	if len(lost)+len(hazards) > 0 {
		tc.e.lose(path, f, what+" has no Argo spelling")
		return "", false
	}
	return out, true
}

// checkout reads the repository into the workspace through an input git artifact.
func (sx *stepCtx) checkout(st *ir.Step, with map[string]string, t actions.Target) {
	tc := sx.tc
	path := tc.jp.path + "/step/" + st.ID
	repo := cloneHost + "{{workflow.parameters.ctx-repo}}.git"
	revision := "{{workflow.parameters.ctx-sha}}"
	useRepo, useSHA := true, true
	if r, ok := with["repository"]; ok {
		out, good := tc.textOrLose(path, caps.StepAction, "the repository input", r)
		if !good {
			return
		}
		repo, revision, useRepo, useSHA = cloneHost+out+".git", "", false, false
	}
	if r, ok := with["ref"]; ok {
		out, good := tc.textOrLose(path, caps.StepAction, "the ref input", r)
		if !good {
			return
		}
		revision, useSHA = out, false
	}
	tc.pp.ctx["ctx-repo"] = tc.pp.ctx["ctx-repo"] || useRepo
	tc.pp.ctx["ctx-sha"] = tc.pp.ctx["ctx-sha"] || useSHA
	g := newMap()
	g.SetStr("repo", repo)
	if revision != "" {
		g.SetStr("revision", revision)
	}
	if d, ok := t.With["depth"].(string); ok && d != "0" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			g.Set("depth", intNode(n))
		} else {
			tc.e.lose(path, caps.StepAction, "the fetch depth "+d+" is no count; the clone is full")
		}
	}
	dir := workspaceMount
	if p, ok := with["path"]; ok && p != "" {
		out, good := tc.textOrLose(path, caps.StepWorkdir, "the checkout path", p)
		if good {
			dir = workspaceMount + "/" + strings.TrimPrefix(out, "/")
		}
	}
	a := newMap()
	a.SetStr("name", unique(sx.takenIn, gitArtifactName, maxName))
	a.SetStr("path", dir)
	a.Set("git", g.N)
	sx.artIn = append(sx.artIn, a.N)
	tc.e.external(path, caps.StepAction, "input git artifact", "a private repository needs usernameSecret and passwordSecret, or sshPrivateKeySecret, on the git artifact; the file names none")
}

// pathLines reads a with value that is one path or a list of paths.
func pathLines(v any) []string {
	switch p := v.(type) {
	case string:
		if strings.TrimSpace(p) == "" {
			return nil
		}
		return []string{strings.TrimSpace(p)}
	case []string:
		return p
	}
	return nil
}

// artifactPath places a path under the workspace.
func artifactPath(p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return workspaceMount + "/" + strings.TrimPrefix(p, "./")
}

// outputNames lists the names of the output artifacts of one upload: the first path takes name, a later path a
// numbered name. It records each in taken, which holds the output names of one template.
func outputNames(taken map[string]bool, name string, paths []string) []string {
	if len(paths) == 0 {
		paths = []string{name}
	}
	var out []string
	for i := range paths {
		n := name
		if i > 0 {
			n = name + "-" + strconv.Itoa(i+1)
		}
		out = append(out, unique(taken, n, maxName))
	}
	return out
}

// addOutputArtifacts adds one output artifact per path of name. Argo saves one path per artifact, so a second
// path gets a numbered name.
func (sx *stepCtx) addOutputArtifacts(path, name string, paths []string, expire string) {
	tc := sx.tc
	if len(paths) == 0 {
		paths = []string{name}
	}
	for i, n := range outputNames(sx.taken, name, paths) {
		p := paths[i]
		if n != name {
			tc.e.emulate(path, caps.JobArtifacts, "one artifact per path", "Argo saves one path per output artifact; the path "+p+" is the artifact "+n)
		}
		if globChars.MatchString(p) {
			tc.e.lose(path, caps.JobArtifacts, "the path "+p+" holds a glob, which Argo does not expand")
		}
		a := newMap()
		a.SetStr("name", n)
		a.SetStr("path", artifactPath(p))
		sx.artOut = append(sx.artOut, a.N)
	}
	if expire != "" {
		tc.e.lose(path, caps.JobArtifacts, "the expiry "+expire+" has no form; the artifact repository sets retention")
	}
}

// upload saves the paths of an upload action as output artifacts.
func (sx *stepCtx) upload(path string, t actions.Target) {
	name, _ := t.With["name"].(string)
	if name == "" {
		name = "artifact"
	}
	sx.addOutputArtifacts(path, safeName(name), pathLines(t.With["path"]), "")
}

func safeName(s string) string {
	out := strings.Trim(invalidParam.ReplaceAllString(s, "-"), "-")
	if out == "" {
		return "artifact"
	}
	return out
}

// uploadsOf lists the output artifact names that a job writes, in the order and with the numbering that the
// emitter gives them: the upload steps first, then the job artifacts.
func (e *emitter) uploadsOf(j *ir.Job) []string {
	var out []string
	taken := map[string]bool{}
	for _, st := range j.Steps {
		if st.Action == nil || st.Action.Kind != ir.ActionRepo {
			continue
		}
		with := map[string]string{}
		for k, v := range st.With {
			if s, ok := valueCanon(v); ok {
				with[k] = s
			}
		}
		if t, ok := actions.Lookup(*st.Action, dialect, with); ok && t.Kind == actions.KindUpload {
			n, _ := t.With["name"].(string)
			if n == "" {
				n = "artifact"
			}
			out = append(out, outputNames(taken, safeName(n), pathLines(t.With["path"]))...)
		}
	}
	for _, a := range j.Artifacts {
		out = append(out, outputNames(taken, safeName(a.Name), a.Paths)...)
	}
	return out
}

// ancestors lists the jobs that j needs, directly or not.
func (pp *plan) ancestors(j *ir.Job) []*jobPlan {
	var out []*jobPlan
	seen := map[string]bool{}
	var walk func(*ir.Job)
	walk = func(x *ir.Job) {
		for _, n := range x.Needs {
			if jp, ok := pp.byID[n.JobID]; ok && !seen[n.JobID] {
				seen[n.JobID] = true
				out = append(out, jp)
				walk(jp.j)
			}
		}
	}
	walk(j)
	return out
}

// inputArtifact adds an input artifact that loads from an output artifact of another task.
func (sx *stepCtx) inputArtifact(from *jobPlan, srcName, dst string) {
	tc := sx.tc
	name := unique(sx.takenIn, srcName, maxName)
	a := newMap()
	a.SetStr("name", name)
	a.SetStr("path", dst)
	sx.artIn = append(sx.artIn, a.N)
	b := newMap()
	b.SetStr("name", name)
	b.SetStr("from", "{{tasks."+from.task+".outputs.artifacts."+srcName+"}}")
	sx.argArts = append(sx.argArts, b.N)
	tc.needed[from.j.ID] = true
}

// download loads the artifact of an upload from a job that this job needs.
func (sx *stepCtx) download(path string, t actions.Target) {
	tc := sx.tc
	name, _ := t.With["name"].(string)
	name = safeName(name)
	dst := workspaceMount
	if p, ok := t.With["path"].(string); ok && p != "" {
		dst = artifactPath(p)
	}
	for _, jp := range tc.pp.ancestors(tc.jp.j) {
		if tc.jp.finally && !jp.finally {
			continue
		}
		for _, n := range tc.e.uploadsOf(jp.j) {
			if n == name {
				sx.dests[dst] = true
				sx.inputArtifact(jp, name, dst)
				if i := slices.IndexFunc(tc.jp.j.Needs, func(n ir.Need) bool { return n.JobID == jp.j.ID }); i < 0 || !tc.jp.j.Needs[i].Artifacts {
					sx.act.Needs = append(sx.act.Needs, jp.j.ID)
				}
				return
			}
		}
	}
	tc.e.lose(path, caps.StepAction, "no job that this job needs uploads an artifact named "+name+"; the download is dropped")
}

// needArtifacts loads every artifact of the jobs that a need names with artifacts.
func (sx *stepCtx) needArtifacts() {
	tc := sx.tc
	for _, n := range tc.jp.j.Needs {
		if !n.Artifacts {
			continue
		}
		jp, ok := tc.pp.byID[n.JobID]
		if !ok {
			continue
		}
		if tc.jp.finally && !jp.finally {
			tc.e.lose(tc.jp.path, caps.JobNeedsArtifacts, "an exit handler task cannot load an artifact of the entry DAG ("+jp.task+"); the artifacts of "+n.JobID+" are not loaded")
			continue
		}
		for _, name := range tc.e.uploadsOf(jp.j) {
			dst := artifactsDir + "/" + name
			if sx.dests[dst] {
				dst = artifactsDir + "/" + jp.task + "/" + name
			}
			sx.dests[dst] = true
			sx.inputArtifact(jp, name, dst)
		}
	}
}

var _ = yaml.Node{}
