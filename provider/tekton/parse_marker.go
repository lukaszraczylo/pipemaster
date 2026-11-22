package tekton

import (
	"encoding/json"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
)

// scheduleFilter reads the filter that the emitter writes for a CronJob trigger.
var scheduleFilter = regexp.MustCompile(`^header\.canonical\('` + scheduleHeader + `'\) == '([^']*)'$`)

// markedFilters reads the filter markers in the head of a document.
func markedFilters(head string) []filterArg {
	var out []filterArg
	args := emitkit.ParseArgs([]byte(head))
	for _, f := range []caps.Feature{caps.TriggerPush, caps.TriggerPullRequest, caps.TriggerTag} {
		for u, lines := range args {
			if u.Feature != f || len(lines) == 0 {
				continue
			}
			var a filterArg
			if json.Unmarshal([]byte(lines[0]), &a) == nil {
				out = append(out, a)
			}
		}
	}
	return out
}

// githubKind is the IR kind that the event types of the github interceptor give for a filter kind.
func githubKind(k ir.TriggerKind) ir.TriggerKind {
	if k == ir.TriggerTag {
		return ir.TriggerPush
	}
	return k
}

// celFilterOf restores the trigger that the emitter wrote from the CEL interceptor, when the filter and the
// overlays are exactly what the marker produces. Otherwise it keeps the filter as opaque text.
func (p *project) celFilterOf(lt listenerTrigger, k *sink, in interceptor, kinds []ir.TriggerKind) *filterArg {
	filter, _ := text(in.params[paramFilter])
	if m := scheduleFilter.FindStringSubmatch(filter); m != nil {
		a := filterArg{Kind: ir.TriggerSchedule, Name: m[1]}
		if overlaysMatch(in, a.Kind) {
			return &a
		}
		p.keepFilter(lt, k, in, filter, "marker ignored: the overlays were edited after pipemaster wrote them; ")
		return nil
	}
	name, _ := text(lt.m.Vals["name"])
	for _, a := range markedFilters(lt.doc.Head) {
		if a.Name != name {
			continue
		}
		want, ok := celFilter(a)
		if !ok {
			want = "false"
		}
		switch {
		case want != filter:
			p.keepFilter(lt, k, in, filter, "marker ignored: the filter was edited after pipemaster wrote it; ")
		case len(kinds) != 1 || kinds[0] != githubKind(a.Kind):
			p.keepFilter(lt, k, in, filter, "marker ignored: the event types were edited after pipemaster wrote them; ")
		case !overlaysMatch(in, a.Kind):
			p.keepFilter(lt, k, in, filter, "marker ignored: the overlays were edited after pipemaster wrote them; ")
		default:
			return &a
		}
		return nil
	}
	why := ""
	for _, a := range markedFilters(lt.doc.Head) {
		if want, ok := celFilter(a); ok && want == filter {
			why = "marker ignored: the trigger was renamed after pipemaster wrote it; "
		}
	}
	p.keepFilter(lt, k, in, filter, why)
	return nil
}

// overlaysMatch checks that every overlay is the context expression that the emitter writes for the kind.
func overlaysMatch(in interceptor, kind ir.TriggerKind) bool {
	list, _ := in.params[paramOverlays].([]any)
	for _, e := range list {
		m, ok := asMap(e)
		if !ok {
			return false
		}
		key, _ := text(valueOf(m, "key"))
		ex, _ := text(valueOf(m, "expression"))
		path, ok := ctxparam.Ref(strings.ReplaceAll(key, "_", "-"))
		if !ok || ctxOverlay(kind, strings.Join(path, ".")) != ex {
			return false
		}
	}
	return true
}

// scheduleTag finds the pipeline name in the schedule header that a CronJob of the emitter posts.
var scheduleTag = regexp.MustCompile(scheduleHeader + `: ([^\s"']+/[0-9]+)`)

// scheduleOwner names the pipeline that a CronJob of the emitter triggers, or "".
func scheduleOwner(spec *omap) string {
	pl, _, _ := strings.Cut(scheduleTagOf(spec), "/")
	return pl
}

// scheduleTagOf finds the schedule header value that a CronJob posts, or "".
func scheduleTagOf(spec *omap) string {
	var found string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case *omap:
			for _, k := range t.Order {
				walk(t.Vals[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case scal:
			if m := scheduleTag.FindStringSubmatch(t.Text()); m != nil && found == "" {
				found = m[1]
			}
		}
	}
	walk(spec)
	return found
}

// workspaceOf reads the workspace marker of the Task that the PipelineTask runs. It returns the marker only
// when the PipelineTask, the Task workspaces and the copy steps are the ones that the emitter writes.
func (p *project) workspaceOf(pt *omap, path string) *workspaceArg {
	ref, _ := asMap(valueOf(pt, "taskRef"))
	name, _ := text(valueOf(ref, "name"))
	doc, ok := p.tasks[name]
	if !ok {
		return nil
	}
	arg, found := markedWorkspace(doc.Head)
	if !found {
		return nil
	}
	spec, _ := asMap(doc.Top.Vals["spec"])
	why := ""
	switch {
	case !isWorkspaceBinding(valueOf(pt, "workspaces")):
		why = "the PipelineTask workspaces were edited"
	case !isWorkspaceMount(valueOf(spec, "workspaces")):
		why = "the Task workspaces were edited"
	case !copyStepsMatch(valueOf(spec, "steps"), arg):
		why = "a copy step was edited"
	}
	if why != "" {
		p.rep.Add(report.Entry{
			IRPath: path, Feature: caps.JobArtifacts, Level: caps.Lost, Strategy: "marker ignored",
			Detail: "marker ignored: " + why + " after pipemaster wrote it; the workspace and the copy steps are kept as written", SourceLoc: doc.Loc(0),
		})
		return nil
	}
	return &arg
}

func markedWorkspace(head string) (workspaceArg, bool) {
	args := emitkit.ParseArgs([]byte(head))
	for _, f := range []caps.Feature{caps.JobCache, caps.JobArtifacts, caps.JobNeedsArtifacts} {
		for u, lines := range args {
			var a workspaceArg
			if u.Feature == f && len(lines) > 0 && json.Unmarshal([]byte(lines[0]), &a) == nil && a.Workspace == workspaceName {
				return a, true
			}
		}
	}
	return workspaceArg{}, false
}

// onlyKeys reports whether the mapping holds exactly the given keys with the given text values.
func onlyKeys(v any, want map[string]string) bool {
	m, ok := asMap(v)
	if !ok || len(m.Order) != len(want) {
		return false
	}
	for k, w := range want {
		if got, ok := text(m.Vals[k]); !ok || got != w {
			return false
		}
	}
	return true
}

func isWorkspaceBinding(v any) bool {
	l, _ := v.([]any)
	return len(l) == 1 && onlyKeys(l[0], map[string]string{"name": workspaceName, "workspace": workspaceName})
}

func isWorkspaceMount(v any) bool {
	l, _ := v.([]any)
	return len(l) == 1 && onlyKeys(l[0], map[string]string{"name": workspaceName, "mountPath": workspaceMount})
}

// copyStepsMatch checks the scripts of the steps that the emitter wrote to copy artifacts.
func copyStepsMatch(v any, a workspaceArg) bool {
	l, _ := v.([]any)
	seen := map[string]string{}
	for _, e := range l {
		m, ok := asMap(e)
		name, _ := text(valueOf(m, "name"))
		if ok && (name == saveStep || name == restoreStep) {
			seen[name], _ = text(m.Vals["script"])
		}
	}
	for name, want := range map[string]string{saveStep: a.Save, restoreStep: a.Restore} {
		got, has := seen[name]
		if (want != "") != has || (has && got != want) {
			return false
		}
	}
	return true
}

func isCopyStep(m *omap) bool {
	name, _ := text(valueOf(m, "name"))
	return name == saveStep || name == restoreStep
}

// restoreWorkspace puts back the job settings that the shared workspace stood in for.
func (b *jobBuilder) restoreWorkspace() {
	a := b.ws
	b.job.Cache = append(b.job.Cache, a.Cache...)
	b.job.Artifacts = append(b.job.Artifacts, a.Artifacts...)
	for _, id := range a.Needs {
		for i := range b.job.Needs {
			if b.job.Needs[i].JobID == id {
				b.job.Needs[i].Artifacts = true
			}
		}
	}
}

// withoutWorkspace drops the pipeline workspace that the emitter declares for the shared workspace.
func withoutWorkspace(v any) any {
	l, ok := v.([]any)
	if !ok {
		return v
	}
	var out []any
	for _, e := range l {
		if m, isMap := asMap(e); isMap && onlyKeys(m, map[string]string{"name": workspaceName}) {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ctxNamesOf(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, e := range l {
		n, _ := text(valueOf(func() *omap { m, _ := asMap(e); return m }(), "name"))
		if path, ok := ctxparam.Ref(n); ok {
			out = append(out, strings.Join(path, "."))
		}
	}
	return out
}

// regenerate writes again the document that the emitter wrote, from the facts the document itself states.
func regenerate(d *rawDoc) *outDoc {
	meta, _ := asMap(d.Top.Vals["metadata"])
	name, _ := text(valueOf(meta, "name"))
	spec, _ := asMap(d.Top.Vals["spec"])
	e := &emitter{}
	switch d.Kind {
	case "TriggerBinding":
		return e.bindingDoc(name, ctxNamesOf(valueOf(spec, "params")))
	case kindTriggerTemplate:
		rts, _ := valueOf(spec, "resourcetemplates").([]any)
		if len(rts) != 1 {
			return nil
		}
		run, _ := asMap(rts[0])
		rs, _ := asMap(valueOf(run, "spec"))
		ref, _ := asMap(valueOf(rs, "pipelineRef"))
		pipe, _ := text(valueOf(ref, "name"))
		return e.templateDoc(&plan{name: pipe, ws: valueOf(rs, "workspaces") != nil}, name, ctxNamesOf(valueOf(spec, "params")))
	case kindCronJob:
		sched, _ := text(valueOf(spec, "schedule"))
		return cronJobDoc(name, sched, scheduleTagOf(spec))
	}
	for _, g := range rbacDocs() {
		gm, _ := asMap(plain2(g.Root))
		if kind, _ := text(valueOf(gm, "kind")); gm != nil && kind == d.Kind {
			if gmeta, _ := asMap(valueOf(gm, "metadata")); gmeta != nil {
				if n, _ := text(valueOf(gmeta, "name")); n == name {
					return g
				}
			}
		}
	}
	return nil
}

// plain2 reads a node that the emitter built as the maps that the parser reads.
func plain2(n *yaml.Node) any {
	b, err := marshalDocs([]*outDoc{{Root: n}})
	if err != nil {
		return nil
	}
	docs, err := readDocsBytes(b)
	if err != nil || len(docs) == 0 {
		return nil
	}
	return docs[0].Top
}

// unchanged reports whether a document that carries the generated marker is still what the emitter writes.
func unchanged(d *rawDoc) bool {
	gen := regenerate(d)
	if gen == nil {
		return false
	}
	want, err1 := json.Marshal(plain(plain2(gen.Root)))
	got, err2 := json.Marshal(plain(d.Top))
	return err1 == nil && err2 == nil && string(got) == string(want)
}

func isGenerated(d *rawDoc) bool {
	return strings.Contains(d.Head, strings.TrimPrefix(generatedMarker, "# "))
}

// dropGenerated removes the documents that the emitter wrote whole and nobody edited from the resources.
func (p *project) dropGenerated() {
	var kept []*rawDoc
	for _, d := range p.resources {
		switch {
		case !isGenerated(d):
			kept = append(kept, d)
		case unchanged(d):
		default:
			p.rep.Add(report.Entry{
				IRPath: "pipeline/", Feature: caps.ExtPassthrough, Level: caps.Lost, Strategy: "marker ignored",
				Detail: "marker ignored: the " + d.Kind + " was edited after pipemaster wrote it; it is kept in Ext", SourceLoc: d.Loc(0),
			})
			kept = append(kept, d)
		}
	}
	p.resources = kept
}

// pruneGenerated removes the Ext keys that equal what the emitter writes for a trigger it restored.
func pruneGenerated(vals map[string]any, trigger, pipeline, listener string) {
	same := func(key string, want any) {
		if a, err := json.Marshal(vals[key]); err == nil {
			if b, err := json.Marshal(want); err == nil && string(a) == string(b) {
				delete(vals, key)
			}
		}
	}
	same("listener", listener)
	same("name", trigger)
	same("template", fit(pipeline+templateNameSuffix, pipeline+templateNameSuffix, maxName))
	same("bindings", []any{map[string]any{"ref": fit(pipeline+bindingNameSuffix, pipeline+bindingNameSuffix, maxName)}})
	same("serviceAccountName", triggersAccount)
	if _, kept := vals["serviceAccountName"]; !kept {
		delete(vals, extSAScope)
	}
}

// hasDefaultSecret reports whether the github interceptor carries the secretRef that the emitter writes.
func hasDefaultSecret(in interceptor) bool {
	v, ok := in.params[paramSecretRef]
	return ok && onlyKeys(v, map[string]string{"secretName": githubSecretName, "secretKey": githubSecretKey})
}
