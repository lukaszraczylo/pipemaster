package argo

import (
	"regexp"
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
	g.setStr("repo", repo)
	if revision != "" {
		g.setStr("revision", revision)
	}
	if d, ok := t.With["depth"].(string); ok && d != "0" {
		if n, err := strconv.Atoi(d); err == nil && n > 0 {
			g.set("depth", intNode(n))
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
	a.setStr("name", unique(sx.taken, gitArtifactName, maxName))
	a.setStr("path", dir)
	a.set("git", g.n)
	sx.artIn = append(sx.artIn, a.n)
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

// addOutputArtifacts adds one output artifact per path of name. Argo saves one path per artifact, so a second
// path gets a numbered name.
func (sx *stepCtx) addOutputArtifacts(path, name string, paths []string, expire string) {
	tc := sx.tc
	if len(paths) == 0 {
		paths = []string{name}
	}
	for i, p := range paths {
		n := name
		if i > 0 {
			n = name + "-" + strconv.Itoa(i+1)
			tc.e.emulate(path, caps.JobArtifacts, "one artifact per path", "Argo saves one path per output artifact; the path "+p+" is the artifact "+n)
		}
		if globChars.MatchString(p) {
			tc.e.lose(path, caps.JobArtifacts, "the path "+p+" holds a glob, which Argo does not expand")
		}
		a := newMap()
		a.setStr("name", unique(sx.taken, n, maxName))
		a.setStr("path", artifactPath(p))
		sx.artOut = append(sx.artOut, a.n)
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

// uploadsOf lists the artifact names that a job writes, from its artifacts and its upload steps.
func (e *emitter) uploadsOf(j *ir.Job) []string {
	var out []string
	for _, a := range j.Artifacts {
		out = append(out, safeName(a.Name))
	}
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
			out = append(out, safeName(n))
		}
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
	name := unique(sx.taken, srcName, maxName)
	a := newMap()
	a.setStr("name", name)
	a.setStr("path", dst)
	sx.artIn = append(sx.artIn, a.n)
	b := newMap()
	b.setStr("name", name)
	b.setStr("from", "{{tasks."+from.task+".outputs.artifacts."+srcName+"}}")
	sx.argArts = append(sx.argArts, b.n)
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
		for _, n := range tc.e.uploadsOf(jp.j) {
			if n == name {
				sx.inputArtifact(jp, name, dst)
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
		for _, name := range tc.e.uploadsOf(jp.j) {
			sx.inputArtifact(jp, name, artifactsDir+"/"+name)
		}
	}
}

var _ = yaml.Node{}
