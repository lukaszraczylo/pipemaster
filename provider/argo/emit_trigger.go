package argo

import (
	"strconv"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	typeGitHubKey = "github"
	contentJSON   = "json"
	apiRBAC       = "rbac.authorization.k8s.io/v1"
	generatedMark = "# pipemaster:generated rbac"
)

// docMap is a document that the emitter edits as a plain map before it writes it.
type docMap map[string]any

// eventDocs holds the EventSources, Sensors and CronWorkflows under construction.
type eventDocs struct {
	sources     map[string]docMap
	sensors     map[string]docMap
	crons       map[string]*cronDoc
	names       map[string]bool
	sourceOrder []string
	sensorOrder []string
	cronOrder   []string
}

type cronDoc struct {
	doc       docMap
	schedules map[int]string
	path      string
}

func newEventDocs() *eventDocs {
	return &eventDocs{sources: map[string]docMap{}, sensors: map[string]docMap{}, crons: map[string]*cronDoc{}, names: map[string]bool{}}
}

// triggers writes the CronWorkflows, the EventSource, the Sensor and the RBAC for the triggers of every pipeline.
func (e *emitter) triggers() error {
	e.events = newEventDocs()
	for _, pp := range e.plans {
		for i := range pp.pl.Triggers {
			e.trigger(pp, i)
		}
	}
	e.flushEvents()
	return nil
}

func (e *emitter) trigger(pp *plan, i int) {
	tr := pp.pl.Triggers[i]
	path := pp.path + "/trigger/" + strconv.Itoa(i)
	e.foreignExt(path, tr.Ext)
	x := map[string]any{}
	if e.own {
		x = extOf(tr.Ext)
	}
	switch tr.Kind {
	case ir.TriggerSchedule:
		e.cron(pp, i, path, tr, x)
	case ir.TriggerPush, ir.TriggerPR, ir.TriggerTag:
		e.eventTrigger(pp, path, tr, x)
	case ir.TriggerAPI:
		e.apiTrigger(pp, path, x)
	case ir.TriggerManual:
		e.manual(pp, path, x)
	}
}

// ownDoc is a plain document of Ext as a doc map.
func ownDoc(v any) docMap {
	m, _ := v.(map[string]any)
	out := docMap{}
	for k, val := range m {
		out[k] = val
	}
	return out
}

// cron adds a schedule to the CronWorkflow of the trigger. A suspended CronWorkflow that an Argo file held
// arrives as an api trigger with no cron; its other schedules keep it suspended.
func (e *emitter) cron(pp *plan, i int, path string, tr ir.Trigger, x map[string]any) {
	ev := e.events
	cw, _ := x["cronWorkflow"].(map[string]any)
	name, _ := cw["name"].(string)
	idx := i
	if n, ok := cw["index"].(int); ok {
		idx = n
	}
	if name == "" {
		name = unique(ev.names, pp.name+"-"+strconv.Itoa(len(ev.cronOrder)+1), maxName)
	}
	c := ev.crons[name]
	if c == nil {
		spec := map[string]any{}
		for k, v := range cw {
			if k != "name" && k != "index" && k != "metadata" {
				spec[k] = v
			}
		}
		if _, ok := spec["workflowSpec"]; !ok {
			spec["workflowSpec"] = map[string]any{"workflowTemplateRef": map[string]any{"name": pp.name}, "serviceAccountName": workflowSA}
			e.needsSA = true
		}
		meta := map[string]any{"name": name}
		if rest, ok := cw["metadata"].(map[string]any); ok {
			for k, v := range rest {
				meta[k] = v
			}
		}
		c = &cronDoc{doc: docMap{apiVersionKey: apiVersion, "kind": kindCron, "metadata": meta, "spec": spec}, path: path, schedules: map[int]string{}}
		ev.crons[name] = c
		ev.cronOrder = append(ev.cronOrder, name)
	}
	if tr.Kind == ir.TriggerSchedule {
		c.schedules[idx] = tr.Cron
		return
	}
	e.lose(path, caps.TriggerAPI, "the CronWorkflow "+name+" is suspended and its schedule is not in the IR; the file keeps it suspended with the schedules of its other triggers")
}

// apiTrigger writes the Sensor trigger or the suspended CronWorkflow that an Argo file held. Any other API
// trigger has no Argo form.
func (e *emitter) apiTrigger(pp *plan, path string, x map[string]any) {
	if _, ok := x["cronWorkflow"]; ok && e.own {
		e.cron(pp, 0, path, ir.Trigger{Kind: ir.TriggerAPI}, x)
		return
	}
	if e.own && (x["trigger"] != nil || x["dependency"] != nil || x["dependencies"] != nil) {
		e.sensorTrigger(path, x)
		return
	}
	e.lose(path, caps.TriggerAPI, "an api trigger that no Argo file held has no Argo form; no Sensor trigger is written")
}

// manual reports the command that starts the template, and writes back the Workflow that an Argo file held.
func (e *emitter) manual(pp *plan, path string, x map[string]any) {
	e.external(path, caps.TriggerManual, "argo submit --from workflowtemplate/"+pp.name, "start a run with: argo submit --from workflowtemplate/"+pp.name+"; the Workflow object needs a ServiceAccount with the rights in .argo/rbac/")
	if w, ok := x["workflow"].(map[string]any); ok && e.own {
		e.keep(path, "Workflow", w)
	}
}

// keep writes a document of Ext under .argo/resources/ unless the emitter wrote an object of that name.
func (e *emitter) keep(path, kind string, doc map[string]any) {
	meta, _ := doc["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	if name == "" {
		name, _ = meta["generateName"].(string)
	}
	key := kind + " " + name
	if e.made[key] {
		return
	}
	e.made[key] = true
	n := e.node(path, kind+" "+name, doc)
	if n == nil {
		return
	}
	file := resourcesDir + strings.ToLower(kind) + "-" + strings.Trim(dnsName(name), "-") + ".yaml"
	e.add(file, &outDoc{root: n})
}

func dnsName(s string) string { return strings.ToLower(invalidParam.ReplaceAllString(s, "-")) }

// keptResources writes the documents that the first pipeline kept, which no IR field models.
func (e *emitter) keptResources() {
	if !e.own || len(e.plans) == 0 {
		return
	}
	pp := e.plans[0]
	list, _ := extOf(pp.pl.Ext)[ExtResources].([]any)
	for _, r := range list {
		doc, _ := r.(map[string]any)
		kind, _ := doc["kind"].(string)
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if kind == kindEventSource && e.mergeSource(name, doc) || kind == kindSensor && e.mergeSensor(name, doc) {
			continue
		}
		e.keep(pp.path, kind, doc)
	}
}

// mergeSource adds the event entries of a kept EventSource to the one the emitter writes under the same name.
func (e *emitter) mergeSource(name string, kept map[string]any) bool {
	dst, ok := e.events.sources[name]
	if !ok {
		return false
	}
	ks, _ := kept["spec"].(map[string]any)
	ds, _ := dst["spec"].(map[string]any)
	for typ, v := range ks {
		if !eventTypes[typ] {
			if _, has := ds[typ]; !has {
				ds[typ] = v
			}
			continue
		}
		evs, _ := v.(map[string]any)
		dm, _ := ds[typ].(map[string]any)
		if dm == nil {
			dm = map[string]any{}
			ds[typ] = dm
		}
		for ev, entry := range evs {
			if _, has := dm[ev]; !has {
				dm[ev] = entry
			}
		}
	}
	return true
}

// mergeSensor adds the triggers and dependencies of a kept Sensor to the one the emitter writes under that name.
func (e *emitter) mergeSensor(name string, kept map[string]any) bool {
	dst, ok := e.events.sensors[name]
	if !ok {
		return false
	}
	ks, _ := kept["spec"].(map[string]any)
	ds, _ := dst["spec"].(map[string]any)
	for _, key := range []string{"dependencies", "triggers"} {
		have, _ := ds[key].([]any)
		for _, v := range ks[key].([]any) {
			dup := false
			vm, _ := v.(map[string]any)
			for _, h := range have {
				hm, _ := h.(map[string]any)
				dup = dup || key == "dependencies" && hm["name"] == vm["name"]
			}
			if !dup {
				have = append(have, v)
			}
		}
		ds[key] = have
	}
	return true
}
