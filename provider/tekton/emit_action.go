package tekton

import (
	"encoding/json"
	"path"
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
	// Wrote is the fingerprint of the step that the file holds for the action; empty when the action wrote none.
	Wrote string `json:"wrote,omitempty"`
	At    int    `json:"at"`
	// Seq orders the steps that stand at the same place, counted from the first step that the file lacks.
	Seq int `json:"seq,omitempty"`
}

// printNode fingerprints written YAML, so the parser can tell a step that is still as written from an edited one.
func printNode(n *yaml.Node) string {
	var v any
	if n == nil || n.Decode(&v) != nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
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

// withoutBookkeeping drops the keys a parser sets only to rebuild its own layout, and the Ext of other providers.
func withoutBookkeeping(x ir.Ext) ir.Ext {
	var out ir.Ext
	for prov, raw := range x {
		if prov != dialect {
			continue
		}
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
func (tc *taskCtx) markAction(b *stepBook, sp string, st *ir.Step, image string, written *wmap) error {
	if st.If != nil && written == nil {
		tc.e.lose(sp, caps.StepIf, "the marker of a step the file does not hold keeps no condition")
	}
	held := *st
	held.If = nil
	held.Ext = withoutBookkeeping(st.Ext)
	body, err := carryYAML(&held)
	if err != nil {
		return err
	}
	var wrote string
	if written != nil {
		wrote = printNode(written.N)
	}
	arg := actionArg{Step: body, At: len(b.nodes), Image: image, Wrote: wrote, Seq: b.seq}
	b.seq++
	return tc.e.markArg(caps.StepAction, sp, arg)
}

// actionStep maps an action step through the actions table. Every mapped action leaves a marker that holds the step.
func (tc *taskCtx) actionStep(b *stepBook, st *ir.Step, sp, name string) (*wmap, error) {
	if st.Action == nil || st.Kind == ir.StepPipelineCall {
		m := tc.extAction(st, sp, name)
		if m == nil {
			return tc.failedAction(b, st, sp, name, "the step has no action reference and no Tekton ref")
		}
		return m, nil
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
	ok = ok && !tc.e.gated[st]
	if !ok {
		why := "no Tekton mapping for action " + actionRef(st.Action)
		tc.dropAction(st, sp, why)
		return tc.failedAction(b, st, sp, name, why)
	}
	tc.e.led.Note(use(sp, caps.StepAction), t.Support, t.Support.Caveat)
	for _, k := range t.Unmapped {
		tc.e.lose(sp, caps.StepAction, "input "+k+" has no Tekton equivalent")
	}
	if t.Support.Level == caps.Lost {
		tc.dropAction(st, sp, t.Support.Caveat)
		return tc.failedAction(b, st, sp, name, "the action has no Tekton form: "+t.Support.Caveat)
	}
	m, image, err := tc.applyTarget(b, st, sp, name, t)
	if err != nil {
		return nil, err
	}
	if m == nil && image == "" && t.Kind != actions.KindCache {
		return tc.failedAction(b, st, sp, name, "action kind "+t.Kind+" has no Tekton form")
	}
	return m, tc.markAction(b, sp, st, image, m)
}

// failedAction writes the step that stands for a step with no Tekton form: it prints the loss and exits 1.
func (tc *taskCtx) failedAction(b *stepBook, st *ir.Step, sp, name, why string) (*wmap, error) {
	m, err := tc.failStep(b, st, sp, name, why)
	if err != nil {
		return nil, err
	}
	return m, tc.markAction(b, sp, st, "", m)
}

func (tc *taskCtx) failStep(b *stepBook, st *ir.Step, sp, name, why string) (*wmap, error) {
	sc := newScope()
	m := newMap()
	m.SetStr("name", name)
	img, _ := tc.stepImage(sc, &ir.Step{}, sp)
	if img != "" {
		m.SetStr("image", img)
	}
	cond := tc.cond(sp, caps.StepIf, st.If, taskScope)
	lines := []string{emitkit.FailLoud(caps.StepAction, sp, why)}
	if guard := andGuard(b.guard, cond.guard); guard != nil {
		var err error
		if lines, err = exprtekton.AddGuard(lines, guard); err != nil {
			return nil, err
		}
		guardEnv(sc, guard)
	}
	m.Set("script", block(strings.Join(lines, "\n")))
	if len(sc.env) > 0 {
		m.Set("env", seqOf(sc.env...))
	}
	tc.stepTail(m, st, cond)
	return m, nil
}

func (tc *taskCtx) dropAction(st *ir.Step, sp, why string) {
	tc.e.lose(sp, caps.StepAction, why)
	tc.unappliedFields(st, sp)
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
		tc.unappliedFields(st, sp)
		return nil
	}
	m := newMap()
	m.SetStr("name", name)
	for _, k := range []string{"ref", "params"} {
		if v, ok := x[k]; ok {
			n, err := plainNode(v)
			if err != nil {
				tc.e.lose(sp, caps.StepAction, err.Error())
				return nil
			}
			m.Set(k, n)
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
		m, err := tc.scriptStep(b, emitkit.ScriptStep(st, t, sp, tc.image == "" && (tc.jp.j.Container == nil || tc.containerRef == "")), sp, name)
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
	url, revision, outPath := "", "", workspaceDir
	// The repository, the ref and the path of the checkout go to the clone parameters.
	given := func(in string) (string, bool, bool) {
		v, ok := t.With[in].(string)
		if !ok || v == "" {
			return "", false, true
		}
		out, good, err := tc.text(newScope(), sp, caps.StepAction, valueText, v)
		return out, true, good && err == nil
	}
	repo, hasRepo, good := given("repository")
	if !good {
		return nil
	}
	if hasRepo {
		url = cloneHost + repo + ".git"
	} else {
		url = cloneHost + param(tc.ctxParam("ctx-repo")) + ".git"
	}
	ref, hasRef, good := given("ref")
	if !good {
		return nil
	}
	switch {
	case hasRef:
		revision = ref
	case !hasRepo:
		revision = param(tc.ctxParam("ctx-sha"))
	}
	if p, hasPath, good := given("path"); !good {
		return nil
	} else if hasPath {
		outPath = path.Join(workspaceDir, p)
	}
	stepRef := newMap()
	stepRef.SetStr("resolver", "git")
	stepRef.Set("params", seqOf(
		nameValue("url", gitCloneURL), nameValue("revision", gitCloneRevision), nameValue("pathInRepo", gitClonePath)))
	params := seqOf(nameValue("url", url))
	if revision != "" {
		params.Content = append(params.Content, nameValue("revision", revision))
	}
	params.Content = append(params.Content, nameValue("output-path", outPath))
	if d, ok := t.With["depth"].(string); ok {
		params.Content = append(params.Content, nameValue("depth", d))
	}
	m := newMap()
	m.SetStr("name", name)
	m.Set("ref", stepRef.N)
	m.Set("params", params)
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
	m.SetStr("name", name)
	img, _ := tc.stepImage(sc, &ir.Step{}, sp)
	if img != "" {
		m.SetStr("image", img)
	}
	m.Set("script", block(strings.Join(append([]string{"set -e"}, lines...), "\n")))
	tc.stepTail(m, st, tc.cond(sp, caps.StepIf, st.If, taskScope))
	return m
}

// unappliedFields reports the settings of a step that the emitter drops, because the settings have no step to sit on.
func (tc *taskCtx) unappliedFields(st *ir.Step, sp string) {
	const why = "the step is lost, so the setting is lost with it"
	for _, f := range []struct {
		what caps.Feature
		set  bool
	}{
		{caps.StepIf, st.If != nil}, {caps.StepEnv, len(st.Env) > 0}, {caps.StepWorkdir, st.WorkDir != ""},
		{caps.StepContinueOnError, st.ContinueOnError}, {caps.StepTimeout, st.Timeout != nil},
		{caps.StepImage, st.Image != nil}, {caps.StepShell, st.Shell != ""},
	} {
		if f.set {
			tc.e.lose(sp, f.what, why)
		}
	}
}
