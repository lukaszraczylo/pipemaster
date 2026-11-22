package tekton

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
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
	if ref, ok := asMap(m.Vals["ref"]); ok {
		in.name, _ = text(ref.Vals["name"])
		if ps, ok := m.Vals["params"].([]any); ok {
			for _, e := range ps {
				pm, _ := asMap(e)
				if n, ok := text(valueOf(pm, "name")); ok {
					in.params[n] = valueOf(pm, "value")
				}
			}
		}
		return in, in.name != ""
	}
	if len(m.Order) == 1 {
		in.name = m.Order[0]
		if body, ok := asMap(m.Vals[in.name]); ok {
			for _, k := range body.Order {
				in.params[k] = body.Vals[k]
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
		meta, _ := asMap(d.Top.Vals["metadata"])
		el, _ := text(valueOf(meta, "name"))
		spec, _ := asMap(d.Top.Vals["spec"])
		list, _ := valueOf(spec, "triggers").([]any)
		var unmapped []any
		for _, e := range list {
			m, ok := asMap(e)
			if !ok {
				unmapped = append(unmapped, plain(e))
				continue
			}
			lt := listenerTrigger{doc: d, m: m, spec: spec, meta: meta, el: el}
			if ref, ok := text(m.Vals["triggerRef"]); ok {
				lt.ref = ref
				if td, found := p.trigDocs[ref]; found {
					if tm, ok := asMap(td.Top.Vals["spec"]); ok {
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
	view, _ := plain(d.Top).(map[string]any)
	if spec, ok := view["spec"].(map[string]any); ok {
		spec["triggers"] = unmapped
	}
	cp := *d
	cp.View = view
	return &cp
}

// templateName reads the TriggerTemplate that a trigger names.
func templateName(m *omap) string {
	tm, ok := asMap(m.Vals["template"])
	if !ok {
		return ""
	}
	if n, ok := text(tm.Vals["ref"]); ok {
		return n
	}
	n, _ := text(tm.Vals["name"])
	return n
}

// pipelineOf finds the pipeline that the PipelineRun of a TriggerTemplate runs.
func (p *project) pipelineOf(tpl string) string {
	d, ok := p.templates[tpl]
	if !ok {
		return ""
	}
	spec, _ := asMap(d.Top.Vals["spec"])
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
	k := newSink(p.rep, lt.doc.File, path)
	k.Out["listener"] = lt.el
	name, _ := text(lt.m.Vals["name"])
	if name != "" {
		k.Out["name"] = name
	}
	if tpl != "" {
		k.Out["template"] = tpl
	}
	if lt.ref != "" {
		k.Out["triggerRef"] = lt.ref
	}
	if lt.refDoc != nil {
		k.KeepMeta("triggerMetadata", lt.refDoc.Top, "Trigger", lt.refDoc.Line)
	}
	if lt.meta != nil {
		k.KeepMeta("listenerMetadata", lt.doc.Top, "EventListener", lt.doc.Line)
	}
	if b := lt.m.Vals["bindings"]; b != nil {
		k.Out["bindings"] = plain(b)
	}
	if sa, ok := text(lt.m.Vals["serviceAccountName"]); ok {
		k.Out["serviceAccountName"] = sa
	} else if sa, ok := text(valueOf(lt.spec, "serviceAccountName")); ok {
		k.Out["serviceAccountName"] = sa
		k.Out[extSAScope] = saScopeSpec
	}
	if lt.spec != nil {
		rest := ynode.NewMap()
		for _, key := range lt.spec.Order {
			if key != "triggers" && key != "serviceAccountName" {
				rest.Set(key, lt.spec.Vals[key], lt.spec.Lines[key])
			}
		}
		if len(rest.Order) > 0 {
			k.Keep("listenerSpec", rest, caps.ExtPassthrough, "EventListener keys beside triggers have no IR field; kept in Ext", lt.doc.Line)
		}
	}
	events, restored := p.interceptors(lt, k, p.pipelineOf(tpl))
	if restored == nil && hasCEL(lt) {
		// A trigger whose filter has no IR form becomes an api trigger, which every provider writes as a trigger
		// that never fires. A push trigger with no branch filter would run on every branch.
		p.reportFilteredTrigger(lt, pl, events)
		events = nil
	}
	switch {
	case restored != nil && restored.Kind == ir.TriggerSchedule:
	case restored != nil:
		pruneGenerated(k.Out, restored.Name, p.pipelineOf(tpl), generatedListenerName(restored.Kind))
		pl.Triggers = append(pl.Triggers, ir.Trigger{
			Kind: restored.Kind, Branches: restored.Branches, Tags: restored.Tags, Paths: restored.Paths, Ext: extFrom(k.Out),
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
	list, _ := lt.m.Vals["interceptors"].([]any)
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
	name, _ := text(lt.m.Vals["name"])
	if restored != nil && restored.Kind == ir.TriggerSchedule {
		// The emitter names the entry after the CronJob; any other name is an edit.
		name = scheduleEntryName(restored.Name, name)
	}
	if restored != nil && (extra || shape != want || p.editedAround(k, name, pipeline, generatedListenerName(restored.Kind))) {
		filter, _ := text(cel.params[paramFilter])
		p.keepFilter(lt, k, *cel, filter, "marker ignored: the trigger was edited after pipemaster wrote it; ")
		restored = nil
	}
	if restored == nil && len(all) > 0 {
		k.Out["interceptors"] = all
		k.Note(caps.ExtPassthrough, "interceptors are kept in Ext in their order", lt.doc.Line)
	}
	return kinds, restored
}

// hasCEL tells a trigger that has a CEL interceptor.
func hasCEL(lt listenerTrigger) bool {
	list, _ := lt.m.Vals["interceptors"].([]any)
	for _, e := range list {
		if in, ok := readInterceptor(e); ok && in.name == interceptorCEL {
			return true
		}
	}
	return false
}

var triggerFeature = map[ir.TriggerKind]caps.Feature{
	ir.TriggerPush: caps.TriggerPush, ir.TriggerTag: caps.TriggerTag, ir.TriggerPR: caps.TriggerPullRequest,
}

// reportFilteredTrigger reports each event kind whose CEL filter has no IR form.
func (p *project) reportFilteredTrigger(lt listenerTrigger, pl *ir.Pipeline, kinds []ir.TriggerKind) {
	name, _ := text(lt.m.Vals["name"])
	for _, kind := range kinds {
		p.rep.Add(report.Entry{
			IRPath: "pipeline/" + pl.ID + "/trigger/" + lt.el, Feature: triggerFeature[kind], Level: caps.Lost, Strategy: strategyExt,
			Detail:    "the trigger " + name + " has a CEL filter with no IR form, so the " + string(kind) + " trigger is not converted; it becomes an api trigger that other providers never start, and the entry is kept in Ext",
			SourceLoc: lt.doc.Loc(0),
		})
	}
}

// editedAround reports whether the listener keys of a marked trigger differ from what the emitter writes.
func (p *project) editedAround(k *sink, trigger, pipeline, listener string) bool {
	tmp := map[string]any{}
	for key, v := range k.Out {
		tmp[key] = v
	}
	pruneGenerated(tmp, trigger, pipeline, listener)
	delete(tmp, "interceptors")
	return len(tmp) > 0
}

// keepFilter keeps a CEL filter as opaque Tekton text.
func (p *project) keepFilter(lt listenerTrigger, k *sink, in interceptor, filter, why string) {
	raw, _ := json.Marshal(map[string]any{"cel": filter})
	k.Out["filter"] = expr.String(expr.Opaque{Dialect: dialect, Text: string(raw)})
	if ov, ok := in.params[paramOverlays]; ok {
		k.Out[extOverlays] = plain(ov)
	}
	p.rep.Add(report.Entry{
		IRPath: k.Path, Feature: caps.ExprOpaque, Level: caps.Lost, Strategy: "kept as opaque Tekton text",
		Detail: why + "a CEL filter has no IR trigger condition; it is kept as opaque Tekton text in Ext: " + filter, SourceLoc: lt.doc.Loc(0),
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
		if m, isMap := asMap(v); isMap && len(m.Order) == 1 {
			if on, _ := text(m.Vals["enabled"]); on == "true" {
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
		out = append(out, ir.Trigger{Kind: kind, Ext: extFrom(k.Out)})
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

var deepCopy = ynode.DeepCopy

// cronJobs maps a CronJob that posts to an EventListener service to schedule triggers of its pipelines.
func (p *project) cronJobs(byName map[string]*ir.Pipeline) {
	for _, d := range p.crons {
		spec, _ := asMap(d.Top.Vals["spec"])
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
		meta, _ := asMap(d.Top.Vals["metadata"])
		name, _ := text(valueOf(meta, "name"))
		rest := ynode.NewMap()
		for _, key := range spec.Order {
			if key != "schedule" {
				rest.Set(key, spec.Vals[key], spec.Lines[key])
			}
		}
		for _, pl := range pls {
			k := newSink(p.rep, d.File, "pipeline/"+pl.ID+"/trigger/"+name)
			if !isGenerated(d) || !unchanged(d) {
				cj := map[string]any{"name": name, "spec": plain(rest)}
				if mr := metaRest(meta); len(mr.Order) > 0 {
					cj["metadata"] = plain(mr)
				}
				k.Out["cronjob"] = cj
				why := ""
				if isGenerated(d) {
					why = "marker ignored: the CronJob was edited after pipemaster wrote it; "
				}
				k.Note(caps.TriggerSchedule, why+"the CronJob that posts to the EventListener has no IR field beside the schedule; kept in Ext", d.Line)
			}
			pl.Triggers = append(pl.Triggers, ir.Trigger{Kind: ir.TriggerSchedule, Cron: sched, Ext: k.Ext()})
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
			for _, k := range t.Order {
				walk(t.Vals[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case scal:
			if m := listenerHost.FindStringSubmatch(" " + t.Text()); m != nil {
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
			meta, _ := asMap(d.Top.Vals["metadata"])
			if n, _ := text(valueOf(meta, "name")); n != el {
				continue
			}
			spec, _ := asMap(d.Top.Vals["spec"])
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

var metaRest = ynode.MetaRest

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
