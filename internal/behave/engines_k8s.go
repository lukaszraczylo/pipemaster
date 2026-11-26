//go:build behave

package behave

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/behave/argostate"
)

const defaultSARole = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: pm-wf-executor}
rules:
  - apiGroups: [argoproj.io]
    resources: [workflowtaskresults]
    verbs: [create, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: pm-wf-executor}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: pm-wf-executor}
subjects: [{kind: ServiceAccount, name: default}]
`

// applyDocs applies each document of a kind list to the namespace.
func (k *Kube) applyDocs(ctx context.Context, ns string, docs []map[string]any, kinds ...string) error {
	for _, d := range docs {
		match := false
		for _, kd := range kinds {
			match = match || kind(d) == kd
		}
		if !match {
			continue
		}
		raw, err := yaml.Marshal(mirrorImages(d))
		if err != nil {
			return err
		}
		if err := k.applyRetry(ctx, ns, string(raw)); err != nil {
			return fmt.Errorf("apply %s %s: %w", kind(d), name(d), err)
		}
	}
	return nil
}

const (
	webhookAttempts = 18
	webhookWait     = 10 * time.Second
	imageMirror     = "mirror.gcr.io/"
)

// applyRetry retries while the Tekton webhook is not ready, which the cluster start does not wait for.
func (k *Kube) applyRetry(ctx context.Context, ns, doc string) error {
	var last error
	for i := 0; i < webhookAttempts; i++ {
		out, code, err := k.env.Kubectl(ctx, kubeShort, doc, "-n", ns, "apply", "-f", "-")
		if err == nil && code == 0 {
			return nil
		}
		last = fmt.Errorf("%v %s", err, tail(out, 400))
		if !strings.Contains(out, "webhook") {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(webhookWait):
		}
	}
	return last
}

// mirrorImages points Docker Hub image references at a mirror, because Docker Hub rate limits the cluster pulls.
func mirrorImages(v map[string]any) map[string]any {
	var walk func(x any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, e := range t {
				if s, ok := e.(string); ok && k == "image" {
					t[k] = mirrorRef(s)
				} else {
					t[k] = walk(e)
				}
			}
		case []any:
			for i, e := range t {
				t[i] = walk(e)
			}
		}
		return x
	}
	return walk(v).(map[string]any)
}

func mirrorRef(ref string) string {
	first, _, hasSlash := strings.Cut(ref, "/")
	switch {
	case !hasSlash:
		return imageMirror + "library/" + ref
	case strings.ContainsAny(first, ".:") || first == "localhost":
		return ref
	}
	return imageMirror + ref
}

// create creates one object and returns its resource name.
func (k *Kube) create(ctx context.Context, ns string, text string) (string, error) {
	out, code, err := k.env.Kubectl(ctx, kubeShort, text, "-n", ns, "create", "-f", "-", "-o", "name")
	if err != nil || code != 0 {
		return "", fmt.Errorf("create: %v %s", err, tail(out, 400))
	}
	return strings.TrimSpace(out), nil
}

// ---- Tekton

type tektonEngine struct {
	env  *Env
	kube *Kube
}

func (t *tektonEngine) Run(ctx context.Context, w *Workspace) (*Trace, error) {
	dir := filepath.Join(w.Dir, ".tekton")
	if !exists(dir) {
		return nil, blocked("no .tekton directory in the converted output")
	}
	if err := t.kube.Start(ctx); err != nil {
		return nil, blocked("the Kiri cluster is not available: %v", err)
	}
	docs, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	if clonesGitHub(docs) {
		return nil, blocked("the converted output clones the repository from %s and the harness has no git host", cloneHostURL)
	}
	evType, body := githubPayload(w)
	runs, err := t.pipelineRuns(docs, w, evType, body)
	if err != nil {
		return nil, blocked("%v", err)
	}
	ns, cleanup, err := t.kube.Namespace(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := t.kube.applyDocs(ctx, ns, docs, "Task", "Pipeline"); err != nil {
		return nil, err
	}
	start := time.Now()
	var names []string
	for _, r := range runs {
		n, err := t.kube.create(ctx, ns, r)
		if err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	tr := &Trace{}
	for _, n := range names {
		exit, err := t.kube.waitStatus(ctx, ns, n, "{.status.conditions[0].status}", map[string]int{"True": 0, "False": 1})
		if err != nil {
			return nil, err
		}
		if exit != 0 {
			tr.Exit = exit
		}
	}
	tr.Log, tr.Markers = collect(ctx, t.kube, ns, "tekton.dev/pipelineRun")
	tr.Duration = time.Since(start)
	return tr, nil
}

func collect(ctx context.Context, k *Kube, ns, selector string) (string, []string) {
	raw, lines := k.collectLogs(ctx, ns, selector)
	return raw, markers(lines)
}

// pipelineRuns builds the PipelineRun manifests that the event starts.
func (t *tektonEngine) pipelineRuns(docs []map[string]any, w *Workspace, evType string, body map[string]any) ([]string, error) {
	templates := map[string]map[string]any{}
	var pipeline map[string]any
	hasListener := false
	for _, d := range docs {
		switch kind(d) {
		case "TriggerTemplate":
			templates[name(d)] = d
		case "Pipeline":
			pipeline = d
		case "EventListener":
			hasListener = true
		}
	}
	if pipeline == nil {
		return nil, fmt.Errorf("the output has no Pipeline")
	}
	if !hasListener {
		return []string{directPipelineRun(pipeline, w)}, nil
	}
	fired, err := selectTektonTriggers(docs, evType, body)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fired {
		tpl, ok := templates[f.Template]
		if !ok {
			return nil, fmt.Errorf("trigger template %q is not in the output", f.Template)
		}
		for _, rt := range list(dig(tpl, "spec", "resourcetemplates")) {
			raw, err := yaml.Marshal(rt)
			if err != nil {
				return nil, err
			}
			out = append(out, substituteTemplate(string(raw), list(dig(tpl, "spec", "params")), f.Params))
		}
	}
	return out, nil
}

// directPipelineRun starts the Pipeline without a trigger: ctx-* parameters take the canonical event values.
func directPipelineRun(pipeline map[string]any, w *Workspace) string {
	ctxVals := canonicalCtx(w)
	var params []any
	for _, p := range list(dig(pipeline, "spec", "params")) {
		if v, ok := ctxVals[str(p, "name")]; ok {
			params = append(params, map[string]any{"name": str(p, "name"), "value": v})
		}
	}
	var wss []any
	for _, ws := range list(dig(pipeline, "spec", "workspaces")) {
		wss = append(wss, map[string]any{"name": str(ws, "name"), "emptyDir": map[string]any{}})
	}
	run := map[string]any{
		"apiVersion": "tekton.dev/v1",
		"kind":       "PipelineRun",
		"metadata":   map[string]any{"generateName": "behave-"},
		"spec":       map[string]any{"pipelineRef": map[string]any{"name": name(pipeline)}, "params": params, "workspaces": wss},
	}
	raw, _ := yaml.Marshal(run) // #nosec G104 -- marshalling a literal map cannot fail
	return string(raw)
}

// ---- Argo

type argoEngine struct {
	env  *Env
	kube *Kube
}

func (a *argoEngine) Run(ctx context.Context, w *Workspace) (*Trace, error) {
	dir := filepath.Join(w.Dir, ".argo")
	if !exists(dir) {
		return nil, blocked("no .argo directory in the converted output")
	}
	if err := a.kube.Start(ctx); err != nil {
		return nil, blocked("the Kiri cluster is not available: %v", err)
	}
	docs, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	if clonesGitHub(docs) {
		return nil, blocked("the converted output clones the repository from %s and the harness has no git host", cloneHostURL)
	}
	evType, body := githubPayload(w)
	flows, err := a.workflows(docs, w, evType, body)
	if err != nil {
		return nil, blocked("%v", err)
	}
	ns, cleanup, err := a.kube.Namespace(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := a.kube.applyDocs(ctx, ns, docs, "WorkflowTemplate", "ServiceAccount", "Role", "RoleBinding"); err != nil {
		return nil, err
	}
	if out, code, err := a.kube.env.Kubectl(ctx, kubeShort, defaultSARole, "-n", ns, "apply", "-f", "-"); err != nil || code != 0 {
		return nil, fmt.Errorf("apply default role: %v %s", err, out)
	}
	start := time.Now()
	var names []string
	for _, f := range flows {
		n, err := a.kube.create(ctx, ns, f)
		if err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	tr := &Trace{}
	for _, n := range names {
		exit, err := a.settle(ctx, ns, n)
		if err != nil {
			return nil, err
		}
		if exit != 0 {
			tr.Exit = exit
		}
	}
	tr.Log, tr.Markers = collect(ctx, a.kube, ns, "workflows.argoproj.io/workflow")
	tr.Duration = time.Since(start)
	return tr, nil
}

const (
	argoIdlePolls = 3
	argoPoll      = 2 * time.Second
	argoStop      = `{"spec":{"shutdown":"Stop"}}`
)

// settle waits for the Workflow to end. A manual job is a suspend task that the harness never resumes: when the
// Workflow holds a suspend node, nothing else runs, and that holds for several polls, the harness stops the
// Workflow, as a manual job that no operator starts. The markers that ran stay, and the stop is not a failure.
func (a *argoEngine) settle(ctx context.Context, ns, name string) (int, error) {
	deadline := time.Now().Add(timeoutK8s)
	idle, stopped := 0, false
	for time.Now().Before(deadline) {
		out, code, err := a.kube.env.Kubectl(ctx, kubeShort, "", "-n", ns, "get", name, "-o", "json")
		if err == nil && code == 0 {
			st, err := argostate.Parse(out)
			if err != nil {
				return -1, err
			}
			switch {
			case st.Done():
				if (st.Phase == "Succeeded") || (stopped && !st.Failed) {
					return 0, nil
				}
				return 1, nil
			case st.Idle() && !stopped:
				if idle++; idle >= argoIdlePolls {
					if out, code, err := a.kube.env.Kubectl(ctx, kubeShort, "", "-n", ns, "patch", name, "--type", "merge", "-p", argoStop); err != nil || code != 0 {
						return -1, fmt.Errorf("stop %s: %v %s", name, err, tail(out, 400))
					}
					stopped = true
				}
			default:
				idle = 0
			}
		}
		if err := a.kube.checkDiskPressure(ctx); err != nil {
			return -1, err
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(argoPoll):
		}
	}
	return -1, fmt.Errorf("%s in %s did not finish within %s", name, ns, timeoutK8s)
}

func (a *argoEngine) workflows(docs []map[string]any, w *Workspace, evType string, body map[string]any) ([]string, error) {
	hasSensor := false
	var templates []map[string]any
	for _, d := range docs {
		switch kind(d) {
		case "Sensor":
			hasSensor = true
		case "WorkflowTemplate":
			templates = append(templates, d)
		}
	}
	if !hasSensor {
		if len(templates) != 1 {
			return nil, fmt.Errorf("the output has %d WorkflowTemplates and no Sensor", len(templates))
		}
		return []string{directWorkflow(templates[0], w)}, nil
	}
	flows, err := argoWorkflows(docs, evType, body)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range flows {
		raw, err := yaml.Marshal(mirrorImages(f))
		if err != nil {
			return nil, err
		}
		out = append(out, string(raw))
	}
	return out, nil
}

func directWorkflow(tpl map[string]any, w *Workspace) string {
	ctxVals := canonicalCtx(w)
	var params []any
	for _, p := range list(dig(tpl, "spec", "arguments", "parameters")) {
		if v, ok := ctxVals[str(p, "name")]; ok {
			params = append(params, map[string]any{"name": str(p, "name"), "value": v})
		}
	}
	wf := map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Workflow",
		"metadata":   map[string]any{"generateName": "behave-"},
		"spec": map[string]any{
			"workflowTemplateRef": map[string]any{"name": name(tpl)},
			"arguments":           map[string]any{"parameters": params},
		},
	}
	raw, _ := yaml.Marshal(wf) // #nosec G104 -- marshalling a literal map cannot fail
	return string(raw)
}
