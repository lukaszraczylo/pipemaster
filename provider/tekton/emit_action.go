package tekton

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/actions"
	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// The git-clone StepAction of tektoncd-catalog/git-clone, pinned to a release tag. Read on 2026-11-21: the
// tag v1.7.1 holds stepaction/git-clone/git-clone.yaml (apiVersion tekton.dev/v1beta1, kind StepAction).
const (
	gitCloneURL      = "https://github.com/tektoncd-catalog/git-clone.git"
	gitCloneRevision = "v1.7.1"
	gitClonePath     = "stepaction/git-clone/git-clone.yaml"
	cloneHost        = "https://github.com/"
)

// actionArg carries the step an action became, so the parser can put it back where it stood.
type actionArg struct {
	Step  string `json:"step"`
	Image string `json:"image,omitempty"`
	At    int    `json:"at"`
}

func carryYAML(v any) (string, error) {
	b, err := yaml.Marshal(v)
	return string(b), err
}

// actionRef spells an action reference the way a workflow does, for messages.
func actionRef(a *ir.ActionRef) string {
	switch a.Kind {
	case ir.ActionDocker:
		return "docker://" + a.Image
	case ir.ActionLocal:
		return a.Path
	}
	s := a.Owner + "/" + a.Repo
	if a.Path != "" {
		s += "/" + a.Path
	}
	if a.Ref != "" {
		s += "@" + a.Ref
	}
	return s
}

// valueCanon renders a value as the canonical text the action registry reads.
func valueCanon(v ir.Value) (string, bool) {
	switch v.Kind {
	case ir.ValueSecret:
		return "${{ secrets." + v.Secret + " }}", true
	case ir.ValueExpr:
		parts := []expr.Part{{Expr: v.Expr}}
		if c, ok := v.Expr.(expr.Call); ok {
			if mixed, ok := emitkit.UnmixFormat(c); ok {
				parts = mixed
			}
		}
		s, err := expr.PrintScriptTemplate(parts)
		return s, err == nil
	}
	return v.Lit, true
}

// withoutBookkeeping drops the keys a parser sets only to rebuild its own layout.
func withoutBookkeeping(x ir.Ext) ir.Ext {
	var out ir.Ext
	for prov, raw := range x {
		m, ok := raw.(map[string]any)
		if !ok {
			if out == nil {
				out = ir.Ext{}
			}
			out[prov] = raw
			continue
		}
		kept := map[string]any{}
		for k, v := range m {
			if !emitkit.BookkeepingExt[k] {
				kept[k] = v
			}
		}
		if len(kept) > 0 {
			if out == nil {
				out = ir.Ext{}
			}
			out[prov] = kept
		}
	}
	return out
}

// markAction writes the marker that holds a step the file does not have as written. The step keeps no condition:
// the IR states conditions in the dialect of its origin, and a restored one would read in the wrong dialect.
func (tc *taskCtx) markAction(b *stepBook, sp string, st *ir.Step, image string) error {
	held := *st
	held.If = nil
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		return err
	}
	return tc.e.markArg(caps.StepAction, sp, actionArg{Step: body, At: len(b.nodes), Image: image})
}

// actionStep maps an action step through the actions table. Every mapped action leaves a marker that holds the step.
func (tc *taskCtx) actionStep(b *stepBook, st *ir.Step, sp, name string) (*wmap, error) {
	if st.Action == nil || st.Kind == ir.StepPipelineCall {
		return tc.extAction(st, sp, name), nil
	}
	with := map[string]string{}
	for k, v := range st.With {
		s, ok := valueCanon(v)
		if !ok {
			tc.e.lose(sp, caps.StepAction, "input "+k+" is an expression with no workflow spelling")
			continue
		}
		with[k] = s
	}
	t, ok := actions.Lookup(*st.Action, dialect, with)
	if !ok {
		tc.dropAction(st, sp, "no Tekton mapping for action "+actionRef(st.Action))
		return nil, nil
	}
	tc.e.led.Note(use(sp, caps.StepAction), t.Support, t.Support.Caveat)
	for _, k := range t.Unmapped {
		tc.e.lose(sp, caps.StepAction, "input "+k+" has no Tekton equivalent")
	}
	if t.Support.Level == caps.Lost {
		tc.dropAction(st, sp, t.Support.Caveat)
		return nil, nil
	}
	m, image, err := tc.applyTarget(b, st, sp, name, t)
	if err != nil {
		return nil, err
	}
	return m, tc.markAction(b, sp, st, image)
}

func (tc *taskCtx) dropAction(st *ir.Step, sp, why string) {
	tc.e.lose(sp, caps.StepAction, why)
	ref := string(st.Kind)
	if st.Action != nil {
		ref = actionRef(st.Action)
	}
	tc.e.comment(sp, "step "+sp+" uses "+ref)
}

// extAction writes a StepAction ref that came from a Tekton file, or reports a step with no form.
func (tc *taskCtx) extAction(st *ir.Step, sp, name string) *wmap {
	x := extOf(st.Ext)
	ref, ok := x["ref"]
	if !ok || !tc.e.own {
		tc.e.lose(sp, caps.StepAction, "the step has no action reference and no Tekton ref")
		return nil
	}
	m := newMap()
	m.setStr("name", name)
	for _, k := range []string{"ref", "params"} {
		if v, ok := x[k]; ok {
			n, err := plainNode(v)
			if err != nil {
				tc.e.lose(sp, caps.StepAction, err.Error())
				return nil
			}
			m.set(k, n)
		}
	}
	_ = ref
	tc.stepTail(m, st, tc.cond(sp, caps.StepIf, st.If, taskScope))
	return m
}

// applyTarget applies a mapped action: it writes the step it needs, or changes the Task.
func (tc *taskCtx) applyTarget(b *stepBook, st *ir.Step, sp, name string, t actions.Target) (*wmap, string, error) {
	sc := newScope()
	switch t.Kind {
	case actions.KindImplicit:
		return tc.cloneStep(st, sp, name, t), "", nil
	case actions.KindImage:
		img, _, err := tc.text(sc, sp, caps.StepAction, valueText, t.Image)
		if err != nil {
			return nil, "", err
		}
		tc.image = img
		tc.unapplied(st, sp, "the action selects an image and is not a step")
		return nil, img, nil
	case actions.KindCache:
		tc.useWorkspace()
		tc.unapplied(st, sp, "the action mounts the shared workspace and is not a step")
		return nil, "", nil
	case actions.KindUpload:
		an, _ := t.With["name"].(string)
		return tc.copyStep(st, sp, name, uploadScript(artifactName(an), asStrings(t.With["path"]))), "", nil
	case actions.KindDownload:
		an, _ := t.With["name"].(string)
		dest, _ := t.With["path"].(string)
		return tc.copyStep(st, sp, name, downloadScript(artifactName(an), dest)), "", nil
	case actions.KindScript:
		m, err := tc.scriptStep(b, &ir.Step{ID: st.ID, Kind: ir.StepScript, Script: t.Script, Env: st.Env, WorkDir: st.WorkDir, If: st.If, Timeout: st.Timeout, ContinueOnError: st.ContinueOnError}, sp, name)
		return m, "", err
	}
	tc.e.lose(sp, caps.StepAction, "action kind "+t.Kind+" has no Tekton form")
	return nil, "", nil
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case string:
		return []string{t}
	}
	return nil
}

// unapplied reports the step fields that have nothing to attach to once the action is not a step.
func (tc *taskCtx) unapplied(st *ir.Step, sp, why string) {
	if st.If != nil {
		tc.e.lose(sp, caps.StepIf, "a step condition cannot guard an action that is not a step: "+why)
	}
	if len(st.Env) > 0 {
		tc.e.lose(sp, caps.StepEnv, "step env has no step to attach to: "+why)
	}
	if st.WorkDir != "" {
		tc.e.lose(sp, caps.StepWorkdir, "step working directory has no step to attach to: "+why)
	}
}

// useWorkspace mounts the shared workspace in the Task.
func (tc *taskCtx) useWorkspace() {
	tc.ws = true
	tc.pp.ws = true
}

// cloneStep writes the StepAction ref to git-clone through the git resolver, at a pinned tag.
func (tc *taskCtx) cloneStep(st *ir.Step, sp, name string, t actions.Target) *wmap {
	tc.checkout, tc.pp.checkout = true, true
	repoParam := tc.ctxParam("ctx-repo")
	shaParam := tc.ctxParam("ctx-sha")
	ref := newMap()
	ref.setStr("resolver", "git")
	ref.set("params", seqOf(
		nameValue("url", gitCloneURL), nameValue("revision", gitCloneRevision), nameValue("pathInRepo", gitClonePath)))
	params := seqOf(
		nameValue("url", cloneHost+param(repoParam)+".git"),
		nameValue("revision", param(shaParam)),
		nameValue("output-path", workspaceDir),
	)
	if d, ok := t.With["depth"].(string); ok {
		params.Content = append(params.Content, nameValue("depth", d))
	}
	m := newMap()
	m.setStr("name", name)
	m.set("ref", ref.n)
	m.set("params", params)
	tc.unapplied(st, sp, "the clone step takes no env or working directory")
	tc.stepTail(m, st, tc.cond(sp, caps.StepIf, st.If, taskScope))
	return m
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// artifactName is the directory name of an artifact in the shared workspace.
func artifactName(n string) string {
	n = unsafeName.ReplaceAllString(n, "_")
	if n == "" {
		return "artifact"
	}
	return n
}

// shq quotes s as one shell word.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func uploadScript(name string, paths []string) []string {
	dir := artifactsDirectory + "/" + name
	words := make([]string, len(paths))
	for i, p := range paths {
		words[i] = shq(p)
	}
	return []string{"mkdir -p " + shq(dir), "tar -cf - " + strings.Join(words, " ") + " | tar -xf - -C " + shq(dir)}
}

func downloadScript(name, dest string) []string {
	if dest == "" {
		dest = "."
	}
	dir := artifactsDirectory + "/" + name
	return []string{"mkdir -p " + shq(dest), "tar -cf - -C " + shq(dir) + " . | tar -xf - -C " + shq(dest)}
}

// copyStep writes a step that copies files into or out of the shared workspace.
func (tc *taskCtx) copyStep(st *ir.Step, sp, name string, lines []string) *wmap {
	tc.useWorkspace()
	sc := newScope()
	m := newMap()
	m.setStr("name", name)
	img, _ := tc.stepImage(sc, &ir.Step{}, sp)
	if img != "" {
		m.setStr("image", img)
	}
	m.set("script", block(strings.Join(append([]string{"set -e"}, lines...), "\n")))
	tc.stepTail(m, st, tc.cond(sp, caps.StepIf, st.If, taskScope))
	return m
}

var _ = exprtekton.OpIn
