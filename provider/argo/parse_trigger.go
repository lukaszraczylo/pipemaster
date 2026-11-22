package argo

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

const utcZone = "UTC"

var utcZones = []string{utcZone, "Etc/UTC"}

// runBindings maps a Workflow that only refers to a template to a manual trigger of that template's pipeline.
func (p *project) runBindings(byName map[string]*ir.Pipeline) {
	for _, d := range p.runs {
		spec, _ := asMap(d.Top.Vals["spec"])
		pl := byName[refName(spec)]
		if pl == nil {
			p.resources = append(p.resources, d)
			continue
		}
		meta, _ := asMap(d.Top.Vals["metadata"])
		name := nameOf(meta)
		k := newSink(p.rep, d.File, "pipeline/"+pl.ID+"/trigger/"+name)
		k.Keep("workflow", d.Top, caps.TriggerManual, "Workflow "+name+" runs the template once; the manual trigger keeps the document in Ext", d.Line)
		pl.Triggers = append(pl.Triggers, ir.Trigger{Kind: ir.TriggerManual, Ext: k.Ext()})
	}
}

// cronTriggers maps each CronWorkflow schedule to a schedule trigger of the pipeline that it runs.
func (p *project) cronTriggers(byName map[string]*ir.Pipeline, byCron map[*rawDoc]*ir.Pipeline) {
	for _, d := range p.crons {
		spec, _ := asMap(d.Top.Vals["spec"])
		wspec, _ := asMap(valueOf(spec, "workflowSpec"))
		pl := byCron[d]
		inline := pl != nil
		if !inline {
			pl = byName[refName(wspec)]
		}
		crons := schedulesOf(spec)
		if pl == nil || len(crons) == 0 {
			p.resources = append(p.resources, d)
			continue
		}
		meta, _ := asMap(d.Top.Vals["metadata"])
		name := nameOf(meta)
		k := newSink(p.rep, d.File, "pipeline/"+pl.ID+"/trigger/"+name)
		cw := map[string]any{"name": name}
		if rest := metaRest(meta); len(rest.Order) > 0 {
			cw["metadata"] = plain(rest)
			k.Note(caps.TriggerSchedule, "CronWorkflow "+name+": metadata has no IR field; kept in Ext", d.Line)
		}
		suspended := p.cronKeys(k, cw, spec, inline)
		for i, cron := range crons {
			tr := ir.Trigger{Kind: ir.TriggerSchedule, Cron: cron}
			if suspended {
				tr.Kind = ir.TriggerAPI
			}
			data := ynode.DeepCopy(cw).(map[string]any)
			data["index"] = i
			tr.Ext = ir.Ext{dialect: map[string]any{"cronWorkflow": data}}
			pl.Triggers = append(pl.Triggers, tr)
		}
		if suspended {
			p.rep.Add(report.Entry{
				IRPath: k.Path, Feature: caps.TriggerSchedule, Level: caps.Lost, Strategy: k.Strategy(), SourceLoc: d.Loc(0),
				Detail: "CronWorkflow " + name + " is suspended, so its schedules become an api trigger that other providers never start",
			})
		}
	}
}

// schedulesOf reads schedules, or the legacy schedule.
func schedulesOf(spec *omap) []string {
	if l, ok := stringList(valueOf(spec, "schedules")); ok && len(l) > 0 {
		return l
	}
	if s, ok := text(valueOf(spec, "schedule")); ok {
		return []string{s}
	}
	return nil
}

// cronKeys copies the CronWorkflow keys that the IR cannot hold into cw, and reports whether the cron is suspended.
func (p *project) cronKeys(k *sink, cw map[string]any, spec *omap, inline bool) (suspended bool) {
	for _, key := range spec.Order {
		v := spec.Vals[key]
		switch key {
		case "schedules", "schedule":
			continue
		case "workflowSpec":
			if !inline {
				cw[key] = plain(v)
				k.Note(caps.ExtPassthrough, "CronWorkflow workflowSpec holds the template reference and its arguments; kept in Ext", spec.Lines[key])
			}
			continue
		case "timezone":
			if t, _ := text(v); slices.Contains(utcZones, t) {
				continue
			}
		case "suspend":
			if b, ok := boolOf(v); ok {
				suspended = b
				if !b {
					continue
				}
			}
		}
		cw[key] = plain(v)
		k.Note(caps.TriggerSchedule, "CronWorkflow key "+key+" has no IR field; kept in Ext", spec.Lines[key])
	}
	return suspended
}

// Event types of an EventSource spec; the other spec keys configure the pod.
var eventTypes = map[string]bool{
	"amqp": true, "azureEventsHub": true, "azureQueueStorage": true, "azureServiceBus": true, "bitbucket": true,
	"bitbucketserver": true, "calendar": true, "emitter": true, "file": true, "generic": true, "gerrit": true,
	"github": true, "gitlab": true, "hdfs": true, "kafka": true, "minio": true, "mqtt": true, "nats": true, "nsq": true,
	"pubsub": true, "pulsar": true, "redis": true, "redisStream": true, "resource": true, "sftp": true, "slack": true,
	"sns": true, "sqs": true, "storageGrid": true, "stripe": true, "webhook": true,
}

const typeGithub = "github"

// unusedEvents is the plain EventSource with only the event entries that no trigger consumed; nil when none is left.
func unusedEvents(d *rawDoc, used map[string]bool) any {
	view, _ := plain(d.Top).(map[string]any)
	spec, _ := view["spec"].(map[string]any)
	left := false
	for typ, v := range spec {
		if !eventTypes[typ] {
			continue
		}
		events, _ := v.(map[string]any)
		for ev := range events {
			if used[typ+"/"+ev] {
				delete(events, ev)
			} else {
				left = true
			}
		}
		if len(events) == 0 {
			delete(spec, typ)
		}
	}
	if !left {
		return nil
	}
	return view
}

// sensorTrigger is one trigger of a Sensor with the documents that it reads.
type sensorTrigger struct {
	doc  *rawDoc
	m    *omap
	spec *omap
	meta *omap
	name string
	deps map[string]*omap
	// depOrder lists the dependency names in source order.
	depOrder []string
}

// sensorTriggers maps each Sensor trigger to triggers of the pipeline that its Workflow runs. A trigger that
// maps to no parsed pipeline stays in Ext[resources] inside a copy of its Sensor.
func (p *project) sensorTriggers(byName map[string]*ir.Pipeline) {
	for _, d := range p.sensors {
		meta, _ := asMap(d.Top.Vals["metadata"])
		spec, _ := asMap(d.Top.Vals["spec"])
		st := sensorTrigger{doc: d, spec: spec, meta: meta, name: nameOf(meta), deps: map[string]*omap{}}
		for _, e := range listOf(valueOf(spec, "dependencies")) {
			if m, ok := asMap(e); ok {
				if n, ok := text(m.Vals["name"]); ok {
					st.deps[n] = m
					st.depOrder = append(st.depOrder, n)
				}
			}
		}
		list := listOf(valueOf(spec, "triggers"))
		var unmapped []any
		mapped := 0
		for _, e := range list {
			m, ok := asMap(e)
			if !ok {
				unmapped = append(unmapped, plain(e))
				continue
			}
			t := st
			t.m = m
			if p.sensorTrigger(t, byName) {
				mapped++
			} else {
				unmapped = append(unmapped, plain(e))
			}
		}
		switch {
		case mapped == 0:
			p.resources = append(p.resources, d)
		case len(unmapped) > 0:
			p.resources = append(p.resources, triggerCopy(d, unmapped))
		}
	}
}

// triggerCopy is the Sensor with only the triggers that map to no pipeline.
func triggerCopy(d *rawDoc, unmapped []any) *rawDoc {
	view, _ := plain(d.Top).(map[string]any)
	if spec, ok := view["spec"].(map[string]any); ok {
		spec["triggers"] = unmapped
	}
	cp := *d
	cp.View = view
	return &cp
}

// triggerWorkflow is the Workflow manifest that a Sensor trigger carries, with how the trigger acts on it.
type triggerWorkflow struct {
	res *omap
	// kind is the trigger type: k8s or argoWorkflow.
	kind string
	// op is the operation of the trigger.
	op string
}

// creates is true when the trigger starts a new run: the k8s create operation, or the argoWorkflow submit
// operation. Argo Events v1.9.11 runs a trigger with no operation as create (pkg/sensors/triggers/standard-k8s/
// standard-k8s.go:145-148) or as submit (pkg/sensors/triggers/argo-workflow/argo-workflow.go:100-103).
func (w triggerWorkflow) creates() bool {
	switch w.kind {
	case triggerK8s:
		return w.op == "" || w.op == opCreate
	case triggerArgo:
		return w.op == "" || w.op == opSubmit
	}
	return false
}

const (
	triggerK8s  = "k8s"
	triggerArgo = "argoWorkflow"
	opCreate    = "create"
	opSubmit    = "submit"
)

// workflowOf finds the Workflow manifest that a trigger carries.
func workflowOf(m *omap) (triggerWorkflow, bool) {
	tm, _ := asMap(m.Vals["template"])
	for _, key := range []string{triggerK8s, triggerArgo} {
		body := asMapOr(valueOf(tm, key))
		src, _ := asMap(valueOf(body, "source"))
		res, ok := asMap(valueOf(src, "resource"))
		if k, _ := text(valueOf(res, "kind")); ok && k == kindWorkflow {
			op, _ := text(valueOf(body, "operation"))
			return triggerWorkflow{res: res, kind: key, op: op}, true
		}
	}
	return triggerWorkflow{}, false
}

func asMapOr(v any) *omap {
	m, _ := asMap(v)
	return m
}

var depName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// depsOf reads the dependencies that fire the trigger: its conditions, or all of them.
func (t sensorTrigger) depsOf() ([]string, bool) {
	tm, _ := asMap(t.m.Vals["template"])
	cond, has := text(valueOf(tm, "conditions"))
	if !has || strings.TrimSpace(cond) == "" {
		return t.depOrder, len(t.depOrder) == 1
	}
	var out []string
	for _, n := range strings.Split(cond, "||") {
		n = strings.TrimSpace(n)
		if !depName.MatchString(n) || t.deps[n] == nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, len(out) > 0
}

// sensorTrigger adds the triggers of one Sensor trigger to its pipeline. It reports whether it found a pipeline.
func (p *project) sensorTrigger(t sensorTrigger, byName map[string]*ir.Pipeline) bool {
	wf, found := workflowOf(t.m)
	wspec, _ := asMap(valueOf(wf.res, "spec"))
	pl := byName[refName(wspec)]
	if !found || pl == nil {
		return false
	}
	tname, _ := text(valueOf(asMapOr(t.m.Vals["template"]), "name"))
	if !wf.creates() {
		p.rep.Add(report.Entry{
			IRPath: "pipeline/" + pl.ID + "/trigger/" + t.name, Feature: caps.TriggerAPI, Level: caps.Lost, Strategy: "kept in Ext[" + dialect + "]", SourceLoc: t.doc.Loc(0),
			Detail: "Sensor trigger " + tname + " has trigger type " + wf.kind + " and operation " + wf.op + ", which does not start a run, or has no Workflow to start; it maps to no trigger and stays in Ext",
		})
		return false
	}
	k := newSink(p.rep, t.doc.File, "pipeline/"+pl.ID+"/trigger/"+t.name)
	k.Keep("trigger", t.m, caps.ExtPassthrough, "Sensor trigger "+tname+" (parameters, policy, the Workflow manifest) has no IR field; kept in Ext", t.doc.Line)
	sensor := map[string]any{"name": t.name}
	if rest := metaRest(t.meta); len(rest.Order) > 0 {
		sensor["metadata"] = plain(rest)
	}
	if rest := t.spec.Subset(restKeys(t.spec, "dependencies", "triggers")); len(rest.Order) > 0 {
		sensor["spec"] = plain(rest)
	}
	k.Out["sensor"] = sensor
	k.Note(caps.ExtPassthrough, "Sensor "+t.name+": metadata and spec keys beside dependencies and triggers have no IR field; kept in Ext", t.doc.Line)
	names, ok := t.depsOf()
	var triggers []ir.Trigger
	var used []usedEvent
	failed := ""
	if !ok {
		failed = "its conditions need all of several events or are not an OR of dependency names"
	}
	for _, n := range names {
		if failed != "" {
			break
		}
		got, u, why := p.dependency(t.deps[n], k)
		triggers, failed = append(triggers, got...), why
		used = append(used, u)
	}
	if failed != "" {
		// A trigger whose filter has no IR form becomes an api trigger, which every provider writes as a trigger
		// that never fires. A push trigger with no branch filter would run on every branch.
		p.rep.Add(report.Entry{
			IRPath: k.Path, Feature: caps.TriggerAPI, Level: caps.Lost, Strategy: k.Strategy(), SourceLoc: t.doc.Loc(0),
			Detail: "the Sensor trigger " + tname + " is not converted (" + failed + "); it becomes an api trigger that other providers never start, and the data is kept in Ext",
		})
		if len(names) == 0 {
			names = t.depOrder
		}
		kept := map[string]any{}
		if len(names) == 1 {
			kept["dependency"] = plain(t.deps[names[0]])
		} else {
			deps := map[string]any{}
			for _, n := range names {
				deps[n] = plain(t.deps[n])
			}
			kept["dependencies"] = deps
		}
		triggers, used = []ir.Trigger{{Kind: ir.TriggerAPI, Ext: ir.Ext{dialect: kept}}}, nil
	}
	for _, u := range used {
		if p.srcUsed[u.source] == nil {
			p.srcUsed[u.source] = map[string]bool{}
		}
		p.srcUsed[u.source][u.entry] = true
	}
	for _, tr := range triggers {
		data := ynode.DeepCopy(k.Out).(map[string]any)
		for key, v := range tr.Ext[dialect].(map[string]any) {
			data[key] = v
		}
		tr.Ext = ir.Ext{dialect: data}
		pl.Triggers = append(pl.Triggers, tr)
	}
	return true
}
