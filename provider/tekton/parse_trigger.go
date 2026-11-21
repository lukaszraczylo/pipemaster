package tekton

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	interceptorGitHub = "github"
	interceptorCEL    = "cel"
	paramEventTypes   = "eventTypes"
	paramFilter       = "filter"
	paramOverlays     = "overlays"
	paramChangedFiles = "addChangedFiles"
	paramSecretRef    = "secretRef"
	// extOverlays keeps the overlays of a CEL interceptor whose filter has no marker.
	extOverlays = "overlays"
	// extSAScope says that serviceAccountName came from the EventListener spec, not from the trigger.
	extSAScope       = "serviceAccountScope"
	saScopeSpec      = "spec"
	eventPush        = "push"
	eventPullRequest = "pull_request"
)

// listenerHost reads the EventListener service name in the URL that a cron job posts to.
var listenerHost = regexp.MustCompile(`(?:^|[/\s"'=])el-([a-z0-9]([a-z0-9-]*[a-z0-9])?)(?:[.:/\s"']|$)`)

// interceptor is one interceptor in either form: ref with params, or the legacy name with a mapping.
type interceptor struct {
	params map[string]any
	raw    *omap
	name   string
}

func readInterceptor(v any) (interceptor, bool) {
	m, ok := asMap(v)
	if !ok {
		return interceptor{}, false
	}
	in := interceptor{raw: m, params: map[string]any{}}
	if ref, ok := asMap(m.vals["ref"]); ok {
		in.name, _ = text(ref.vals["name"])
		if ps, ok := m.vals["params"].([]any); ok {
			for _, e := range ps {
				pm, _ := asMap(e)
				if n, ok := text(valueOf(pm, "name")); ok {
					in.params[n] = valueOf(pm, "value")
				}
			}
		}
		return in, in.name != ""
	}
	if len(m.keys) == 1 {
		in.name = m.keys[0]
		if body, ok := asMap(m.vals[in.name]); ok {
			for _, k := range body.keys {
				in.params[k] = body.vals[k]
			}
		}
		return in, true
	}
	return interceptor{}, false
}

// listenerTrigger is one trigger of an EventListener with the EventListener it came from.
type listenerTrigger struct {
	doc *rawDoc
	m   *omap
	// spec is the EventListener spec; meta is its metadata.
	spec *omap
	meta *omap
	// ref and refDoc are the triggerRef name and the Trigger document it names.
	refDoc *rawDoc
	ref    string
	el     string
}

// triggers maps each EventListener trigger to triggers of the pipeline that its template runs. A trigger that
// maps to no parsed pipeline stays in Ext[resources] inside a copy of its EventListener.
func (p *project) triggers(byName map[string]*ir.Pipeline) {
	for _, d := range p.listeners {
		meta, _ := asMap(d.top.vals["metadata"])
		el, _ := text(valueOf(meta, "name"))
		spec, _ := asMap(d.top.vals["spec"])
		list, _ := valueOf(spec, "triggers").([]any)
		var unmapped []any
		for _, e := range list {
			m, ok := asMap(e)
			if !ok {
				unmapped = append(unmapped, plain(e))
				continue
			}
			lt := listenerTrigger{doc: d, m: m, spec: spec, meta: meta, el: el}
			if ref, ok := text(m.vals["triggerRef"]); ok {
				lt.ref = ref
				if td, found := p.trigDocs[ref]; found {
					if tm, ok := asMap(td.top.vals["spec"]); ok {
						lt.m, lt.refDoc = tm, td
					}
				}
			}
			if p.trigger(lt, byName) {
				if lt.refDoc != nil {
					p.trigUsed[lt.ref] = true
				}
			} else {
				unmapped = append(unmapped, plain(e))
			}
		}
		if len(unmapped) > 0 || len(list) == 0 {
			p.resources = append(p.resources, listenerCopy(d, unmapped))
		}
	}
}

// listenerCopy is the EventListener with only the triggers that map to no pipeline.
func listenerCopy(d *rawDoc, unmapped []any) *rawDoc {
	view, _ := plain(d.top).(map[string]any)
	if spec, ok := view["spec"].(map[string]any); ok {
		spec["triggers"] = unmapped
	}
	cp := *d
	cp.view = view
	return &cp
}

// templateName reads the TriggerTemplate that a trigger names.
func templateName(m *omap) string {
	tm, ok := asMap(m.vals["template"])
	if !ok {
		return ""
	}
	if n, ok := text(tm.vals["ref"]); ok {
		return n
	}
	n, _ := text(tm.vals["name"])
	return n
}

// pipelineOf finds the pipeline that the PipelineRun of a TriggerTemplate runs.
func (p *project) pipelineOf(tpl string) string {
	d, ok := p.templates[tpl]
	if !ok {
		return ""
	}
	spec, _ := asMap(d.top.vals["spec"])
	rts, _ := valueOf(spec, "resourcetemplates").([]any)
	for _, e := range rts {
		rm, _ := asMap(e)
		if k, _ := text(valueOf(rm, "kind")); k != kindPipelineRun {
			continue
		}
		rspec, _ := asMap(valueOf(rm, "spec"))
		ref, _ := asMap(valueOf(rspec, "pipelineRef"))
		if n, ok := text(valueOf(ref, "name")); ok {
			return n
		}
	}
	return ""
}

// trigger adds the triggers of one EventListener trigger to its pipeline. It reports whether it found one.
func (p *project) trigger(lt listenerTrigger, byName map[string]*ir.Pipeline) bool {
	tpl := templateName(lt.m)
	pl, ok := byName[p.pipelineOf(tpl)]
	if !ok {
		return false
	}
	path := "pipeline/" + pl.ID + "/trigger/" + lt.el
	k := newSink(p.rep, lt.doc.file, path)
	k.vals["listener"] = lt.el
	name, _ := text(lt.m.vals["name"])
	if name != "" {
		k.vals["name"] = name
	}
	if tpl != "" {
		k.vals["template"] = tpl
	}
	if lt.ref != "" {
		k.vals["triggerRef"] = lt.ref
	}
	if lt.refDoc != nil {
		k.keepMeta("triggerMetadata", lt.refDoc.top, "Trigger", lt.refDoc.line)
	}
	if lt.meta != nil {
		k.keepMeta("listenerMetadata", lt.doc.top, "EventListener", lt.doc.line)
	}
	if b := lt.m.vals["bindings"]; b != nil {
		k.vals["bindings"] = plain(b)
	}
	if sa, ok := text(lt.m.vals["serviceAccountName"]); ok {
		k.vals["serviceAccountName"] = sa
	} else if sa, ok := text(valueOf(lt.spec, "serviceAccountName")); ok {
		k.vals["serviceAccountName"] = sa
		k.vals[extSAScope] = saScopeSpec
	}
	if lt.spec != nil {
		rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
		for _, key := range lt.spec.keys {
			if key != "triggers" && key != "serviceAccountName" {
				rest.set(key, lt.spec.vals[key], lt.spec.lines[key])
			}
		}
		if len(rest.keys) > 0 {
			k.keep("listenerSpec", rest, caps.ExtPassthrough, "EventListener keys beside triggers have no IR field; kept in Ext", lt.doc.line)
		}
	}
	events, restored := p.interceptors(lt, k, p.pipelineOf(tpl))
	switch {
	case restored != nil && restored.Kind == ir.TriggerSchedule:
	case restored != nil:
		pruneGenerated(k.vals, restored.Name, p.pipelineOf(tpl))
		pl.Triggers = append(pl.Triggers, ir.Trigger{
			Kind: restored.Kind, Branches: restored.Branches, Tags: restored.Tags, Paths: restored.Paths, Ext: extFrom(k.vals),
		})
	default:
		pl.Triggers = append(pl.Triggers, buildTriggers(events, k)...)
	}
	return true
}

// interceptors reads the interceptor list. It returns the IR kinds that the github interceptor names, and the
// trigger that a filter marker restores. A CEL filter with no valid marker becomes opaque Tekton text in Ext.
// A trigger that carries anything beyond what the emitter writes keeps the whole list, in order, and the marker
// is ignored.
func (p *project) interceptors(lt listenerTrigger, k *sink, pipeline string) ([]ir.TriggerKind, *filterArg) {
	list, _ := lt.m.vals["interceptors"].([]any)
	var kinds []ir.TriggerKind
	var all []any
	var cel *interceptor
	extra := false
	shape := ""
	for _, e := range list {
		all = append(all, plain(e))
		in, ok := readInterceptor(e)
		switch {
		case ok && in.name == interceptorGitHub:
			ks, whole := githubKinds(in)
			kinds = append(kinds, ks...)
			extra = extra || !whole || !hasDefaultSecret(in)
			shape += "g"
		case ok && in.name == interceptorCEL && cel == nil:
			c := in
			cel = &c
			shape += "c"
		default:
			extra = true
			shape += "o"
		}
	}
	var restored *filterArg
	if cel != nil {
		restored = p.celFilterOf(lt, k, *cel, kinds)
	}
	want := "gc"
	if restored != nil && restored.Kind == ir.TriggerSchedule {
		want = "c"
	}
	name, _ := text(lt.m.vals["name"])
	if restored != nil && restored.Kind == ir.TriggerSchedule {
		// The emitter names the entry after the CronJob; any other name is an edit.
		name = scheduleEntryName(restored.Name, name)
	}
	if restored != nil && (extra || shape != want || p.editedAround(k, name, pipeline)) {
		filter, _ := text(cel.params[paramFilter])
		p.keepFilter(lt, k, *cel, filter, "marker ignored: the trigger was edited after pipemaster wrote it; ")
		restored = nil
	}
	if restored == nil && len(all) > 0 {
		k.vals["interceptors"] = all
		k.note(caps.ExtPassthrough, "interceptors are kept in Ext in their order", lt.doc.line)
	}
	return kinds, restored
}

// editedAround reports whether the listener keys of a marked trigger differ from what the emitter writes.
func (p *project) editedAround(k *sink, trigger, pipeline string) bool {
	tmp := map[string]any{}
	for key, v := range k.vals {
		tmp[key] = v
	}
	pruneGenerated(tmp, trigger, pipeline)
	delete(tmp, "interceptors")
	return len(tmp) > 0
}

// keepFilter keeps a CEL filter as opaque Tekton text.
func (p *project) keepFilter(lt listenerTrigger, k *sink, in interceptor, filter, why string) {
	raw, _ := json.Marshal(map[string]any{"cel": filter})
	k.vals["filter"] = expr.String(expr.Opaque{Dialect: dialect, Text: string(raw)})
	if ov, ok := in.params[paramOverlays]; ok {
		k.vals[extOverlays] = plain(ov)
	}
	p.rep.Add(report.Entry{
		IRPath: k.path, Feature: caps.ExprOpaque, Level: caps.Lost, Strategy: "kept as opaque Tekton text",
		Detail: why + "a CEL filter has no IR trigger condition; it is kept as opaque Tekton text in Ext: " + filter, SourceLoc: lt.doc.loc(0),
	})
}

func dedupe(l []any) []any {
	var out []any
	seen := map[string]bool{}
	for _, v := range l {
		b, _ := json.Marshal(v)
		if !seen[string(b)] {
			seen[string(b)] = true
			out = append(out, v)
		}
	}
	return out
}

// githubKinds maps the eventTypes of the github interceptor. whole is false when the interceptor holds
// anything else than a push or pull_request event type and the changed-files switch.
func githubKinds(in interceptor) (kinds []ir.TriggerKind, whole bool) {
	types, _ := textList(in.params[paramEventTypes])
	n := 1
	if v, ok := in.params[paramChangedFiles]; ok {
		if m, isMap := asMap(v); isMap && len(m.keys) == 1 {
			if on, _ := text(m.vals["enabled"]); on == "true" {
				n++
			}
		}
	}
	if hasDefaultSecret(in) {
		n++
	}
	whole = len(in.params) == n && len(types) > 0
	for _, t := range types {
		switch t {
		case eventPush:
			kinds = append(kinds, ir.TriggerPush)
		case eventPullRequest:
			kinds = append(kinds, ir.TriggerPR)
		default:
			whole = false
		}
	}
	return kinds, whole
}

// buildTriggers makes one trigger per kind, or one api trigger for an endpoint that names no event.
func buildTriggers(kinds []ir.TriggerKind, k *sink) []ir.Trigger {
	if len(kinds) == 0 {
		kinds = []ir.TriggerKind{ir.TriggerAPI}
	}
	var out []ir.Trigger
	for _, kind := range kinds {
		out = append(out, ir.Trigger{Kind: kind, Ext: extFrom(k.vals)})
	}
	return out
}

// extFrom wraps the kept keys of a trigger as its Ext; no key leaves no Ext.
func extFrom(vals map[string]any) ir.Ext {
	if len(vals) == 0 {
		return nil
	}
	return ir.Ext{dialect: cloneMap(vals)}
}

func cloneMap(m map[string]any) map[string]any { return deepCopy(m).(map[string]any) }

// deepCopy copies the maps and lists of an Ext value, so triggers of one listener share no state.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = deepCopy(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopy(e)
		}
		return out
	}
	return v
}

// cronJobs maps a CronJob that posts to an EventListener service to schedule triggers of its pipelines.
func (p *project) cronJobs(byName map[string]*ir.Pipeline) {
	for _, d := range p.crons {
		spec, _ := asMap(d.top.vals["spec"])
		sched, ok := text(valueOf(spec, "schedule"))
		els := listenersIn(spec)
		pls := p.pipelinesOf(els, byName)
		if tagged, ok := byName[scheduleOwner(spec)]; ok {
			pls = []*ir.Pipeline{tagged}
		}
		if !ok || len(pls) == 0 {
			p.resources = append(p.resources, d)
			continue
		}
		meta, _ := asMap(d.top.vals["metadata"])
		name, _ := text(valueOf(meta, "name"))
		rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
		for _, key := range spec.keys {
			if key != "schedule" {
				rest.set(key, spec.vals[key], spec.lines[key])
			}
		}
		for _, pl := range pls {
			k := newSink(p.rep, d.file, "pipeline/"+pl.ID+"/trigger/"+name)
			if !isGenerated(d) || !unchanged(d) {
				cj := map[string]any{"name": name, "spec": plain(rest)}
				if mr := metaRest(meta); len(mr.keys) > 0 {
					cj["metadata"] = plain(mr)
				}
				k.vals["cronjob"] = cj
				why := ""
				if isGenerated(d) {
					why = "marker ignored: the CronJob was edited after pipemaster wrote it; "
				}
				k.note(caps.TriggerSchedule, why+"the CronJob that posts to the EventListener has no IR field beside the schedule; kept in Ext", d.line)
			}
			pl.Triggers = append(pl.Triggers, ir.Trigger{Kind: ir.TriggerSchedule, Cron: sched, Ext: k.ext()})
		}
	}
}

// listenersIn finds the EventListener names in every string of a CronJob spec.
func listenersIn(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case *omap:
			for _, k := range t.keys {
				walk(t.vals[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case scal:
			if m := listenerHost.FindStringSubmatch(" " + t.text()); m != nil {
				out = append(out, m[1])
			}
		}
	}
	walk(v)
	return out
}

// pipelinesOf lists the pipelines that the named EventListeners run, in the order of first use.
func (p *project) pipelinesOf(els []string, byName map[string]*ir.Pipeline) []*ir.Pipeline {
	var out []*ir.Pipeline
	seen := map[*ir.Pipeline]bool{}
	for _, el := range els {
		for _, d := range p.listeners {
			meta, _ := asMap(d.top.vals["metadata"])
			if n, _ := text(valueOf(meta, "name")); n != el {
				continue
			}
			spec, _ := asMap(d.top.vals["spec"])
			list, _ := valueOf(spec, "triggers").([]any)
			for _, e := range list {
				m, ok := asMap(e)
				if !ok {
					continue
				}
				if pl, ok := byName[p.pipelineOf(templateName(m))]; ok && !seen[pl] {
					seen[pl] = true
					out = append(out, pl)
				}
			}
		}
	}
	return out
}

// metaRest is the metadata keys except the name.
func metaRest(meta *omap) *omap {
	rest := &omap{vals: map[string]any{}, lines: map[string]int{}}
	if meta == nil {
		return rest
	}
	for _, key := range meta.keys {
		if key != "name" {
			rest.set(key, meta.vals[key], meta.lines[key])
		}
	}
	return rest
}

// scheduleEntryName gives the name that the emitter writes for the listener entry of a schedule tag
// (pipeline/index), or actual when the tag has no index. The name differs from actual when the entry was renamed.
func scheduleEntryName(tag, actual string) string {
	i := strings.LastIndex(tag, "/")
	if i < 0 {
		return actual
	}
	n := tag[:i] + "-schedule-" + tag[i+1:]
	return fit(n, n, cronNameMax)
}
