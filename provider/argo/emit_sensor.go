package argo

import (
	"reflect"
	"slices"
	"sort"
	"strconv"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/internal/ynode"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	trimHeads   = `{{ trimPrefix "refs/heads/" .Input.body.ref }}`
	trimTags    = `{{ trimPrefix "refs/tags/" .Input.body.ref }}`
	forkCaveat  = "the trigger accepts a pull request from the same repository only; a fork would run its code with every secret"
	placeholder = "the repository owner and name are placeholders; set them in the EventSource"
)

// eventParams maps a context parameter to the event data it reads, per event class. A value entry is a literal.
type eventSrc struct{ key, tmpl, value string }

func ctxSource(name, event string, tag bool) (eventSrc, bool) {
	push := event == eventPush
	switch name {
	case "ctx-ref":
		if push {
			return eventSrc{key: pathRef}, true
		}
		return eventSrc{key: "body.pull_request.head.ref"}, true
	case "ctx-branch", "ctx-ref-name":
		switch {
		case push && tag:
			return eventSrc{tmpl: trimTags}, true
		case push:
			return eventSrc{tmpl: trimHeads}, true
		}
		return eventSrc{key: "body.pull_request.head.ref"}, true
	case "ctx-tag":
		if push && tag {
			return eventSrc{tmpl: trimTags}, true
		}
	case "ctx-event":
		return eventSrc{value: event}, true
	case "ctx-sha":
		if push {
			return eventSrc{key: "body.after"}, true
		}
		return eventSrc{key: "body.pull_request.head.sha"}, true
	case "ctx-repo":
		return eventSrc{key: pathRepo}, true
	case "ctx-actor":
		return eventSrc{key: "body.sender.login"}, true
	case "ctx-pr-number":
		if !push {
			return eventSrc{key: "body.number"}, true
		}
	case "ctx-ref-type":
		if push && tag {
			return eventSrc{value: "tag"}, true
		}
		return eventSrc{value: "branch"}, true
	case "ctx-pr-head-ref":
		if !push {
			return eventSrc{key: "body.pull_request.head.ref"}, true
		}
	case "ctx-pr-base-ref":
		if !push {
			return eventSrc{key: pathBaseRef}, true
		}
	}
	return eventSrc{}, false
}

// depName names a dependency after the pipeline and the event class.
func (ev *eventDocs) depName(pl string, t ir.Trigger) string {
	return unique(ev.names, pl+"-"+string(t.Kind), maxName)
}

// eventTrigger writes the dependency, the Sensor trigger and the EventSource entry of one push, pull request or
// tag trigger. An Argo file that the trigger came from is written back when the IR still equals what it says.
func (e *emitter) eventTrigger(pp *plan, path string, tr ir.Trigger, x map[string]any) {
	if e.own && x["dependency"] != nil && x["eventSource"] != nil && x["trigger"] != nil && e.sameAsFile(tr, x) {
		e.restoreSensor(x)
		return
	}
	if len(tr.Paths.Include)+len(tr.Paths.Exclude) > 0 {
		e.lose(path, caps.TriggerFilterPaths, "a Sensor data filter cannot test the changed files; the trigger runs on every change")
	}
	filters, event, why := triggerFilters(tr)
	if why != "" {
		e.lose(path, filterFeature(tr), "the trigger is not written: "+why)
		return
	}
	ev := e.events
	dn := ev.depName(pp.name, tr)
	dep := docMap{"name": dn, "eventSourceName": eventSourceName, "eventName": event}
	if len(filters) > 0 {
		dep["filters"] = filters
	}
	ev.entry(event)
	e.addSensorTrigger(pp, path, tr, dn, event, dep)
	e.emulate(path, caps.TriggerFilterBranch, "", "")
}

func filterFeature(t ir.Trigger) caps.Feature {
	if t.Kind == ir.TriggerTag {
		return caps.TriggerFilterTags
	}
	return caps.TriggerFilterBranch
}

// entry adds the github event entry of an event class to the EventSource.
func (ev *eventDocs) entry(event string) {
	src := ev.source(eventSourceName)
	spec := src["spec"].(map[string]any)
	gh, _ := spec[typeGitHubKey].(map[string]any)
	if gh == nil {
		gh = map[string]any{}
		spec[typeGitHubKey] = gh
	}
	if _, ok := gh[event]; ok {
		return
	}
	gh[event] = map[string]any{
		"repositories":  []any{map[string]any{"owner": "OWNER", "names": []any{"REPO"}}},
		"webhook":       map[string]any{"endpoint": "/" + event, "port": webhookPort, "method": "POST", "url": ""},
		"events":        []any{event},
		"contentType":   contentJSON,
		"active":        true,
		"webhookSecret": map[string]any{"name": webhookSecret, "key": webhookKey},
	}
}

func (ev *eventDocs) source(name string) docMap {
	if d, ok := ev.sources[name]; ok {
		return d
	}
	d := docMap{apiVersionKey: apiVersion, "kind": kindEventSource, "metadata": map[string]any{"name": name}, "spec": map[string]any{
		"service": map[string]any{"ports": []any{map[string]any{"port": mustInt(webhookPort), "targetPort": mustInt(webhookPort)}}},
	}}
	ev.sources[name] = d
	ev.sourceOrder = append(ev.sourceOrder, name)
	return d
}

func mustInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func (ev *eventDocs) sensor(name string) docMap {
	if d, ok := ev.sensors[name]; ok {
		return d
	}
	d := docMap{apiVersionKey: apiVersion, "kind": kindSensor, "metadata": map[string]any{"name": name}, "spec": map[string]any{
		"template": map[string]any{"serviceAccountName": sensorSA},
	}}
	ev.sensors[name] = d
	ev.sensorOrder = append(ev.sensorOrder, name)
	return d
}

func appendSpec(d docMap, key string, v any) {
	spec := d["spec"].(map[string]any)
	l, _ := spec[key].([]any)
	spec[key] = append(l, v)
}

// addSensorTrigger writes one dependency and its k8s trigger.
func (e *emitter) addSensorTrigger(pp *plan, path string, tr ir.Trigger, dn, event string, dep docMap) {
	ev := e.events
	sensor := ev.sensor(sensorName)
	appendSpec(sensor, "dependencies", dep)
	var params []any
	for i, a := range pp.args {
		if !a.ctx {
			continue
		}
		src, ok := ctxSource(a.name, event, tr.Kind == ir.TriggerTag)
		if !ok {
			continue
		}
		s := map[string]any{"dependencyName": dn}
		switch {
		case src.value != "":
			s["value"] = src.value
		case src.tmpl != "":
			s["dataTemplate"] = src.tmpl
		default:
			s["dataKey"] = src.key
		}
		params = append(params, map[string]any{"src": s, "dest": "spec.arguments.parameters." + strconv.Itoa(i) + ".value"})
	}
	var args []any
	for _, a := range pp.args {
		m := map[string]any{"name": a.name}
		if a.has || a.ctx {
			m["value"] = a.value
		}
		args = append(args, m)
	}
	wf := map[string]any{
		apiVersionKey: apiVersion, "kind": kindWorkflow, "metadata": map[string]any{"generateName": pp.name + "-"},
		"spec": map[string]any{"workflowTemplateRef": map[string]any{"name": pp.name}, "serviceAccountName": workflowSA},
	}
	if len(args) > 0 {
		wf["spec"].(map[string]any)["arguments"] = map[string]any{"parameters": args}
	}
	k8s := map[string]any{"operation": "create", "source": map[string]any{"resource": wf}}
	if len(params) > 0 {
		k8s["parameters"] = params
	}
	trig := map[string]any{"template": map[string]any{"name": dn, "conditions": dn, "k8s": k8s}}
	appendSpec(sensor, "triggers", trig)
	e.needsSA = true
	e.external(path, caps.TriggerPush, "EventSource webhook secret", "Secret "+webhookSecret+" with key "+webhookKey+" must exist and match the GitHub webhook; "+placeholder)
	if tr.Kind == ir.TriggerPR {
		e.emulate(path, caps.TriggerPullRequest, "", forkCaveat)
	}
}

// sameAsFile is true when the IR trigger equals the triggers that the Ext of an Argo file derives.
func (e *emitter) sameAsFile(tr ir.Trigger, x map[string]any) bool {
	got, ok := deriveTriggers(x)
	if !ok {
		return false
	}
	want := tr
	want.Ext = nil
	return slices.ContainsFunc(got, func(g ir.Trigger) bool {
		g.Ext = nil
		return reflect.DeepEqual(g, want)
	})
}

// deriveTriggers reads the dependency of an Ext back through the parser.
func deriveTriggers(x map[string]any) ([]ir.Trigger, bool) {
	dep, _ := x["dependency"].(map[string]any)
	es, _ := x["eventSource"].(map[string]any)
	if dep == nil || es == nil {
		return nil, false
	}
	typ, _ := es["type"].(string)
	event, _ := es["event"].(string)
	spec := map[string]any{typ: map[string]any{event: es["entry"]}}
	if rest, ok := es["spec"].(map[string]any); ok {
		for k, v := range rest {
			spec[k] = v
		}
	}
	srcName, _ := dep["eventSourceName"].(string)
	meta := map[string]any{"name": srcName}
	doc := map[string]any{apiVersionKey: apiVersion, "kind": kindEventSource, "metadata": meta, "spec": spec}
	srcNode, err := plainNode(doc)
	if err != nil {
		return nil, false
	}
	depNode, err := plainNode(dep)
	if err != nil {
		return nil, false
	}
	srcs, err1 := readDocsBytes(mustMarshal(srcNode))
	deps, err2 := readDocsBytes(mustMarshal(depNode))
	if err1 != nil || err2 != nil || len(srcs) != 1 || len(deps) != 1 {
		return nil, false
	}
	rep := &report.Report{}
	p := &project{rep: rep, sources: map[string]*rawDoc{srcName: srcs[0]}}
	k := newSink(rep, "", "")
	trs, _, why := p.dependency(deps[0].Top, k)
	return trs, why == ""
}

// restoreSensor writes the dependency, the Sensor trigger and the EventSource entry that an Argo file held.
func (e *emitter) restoreSensor(x map[string]any) {
	ev := e.events
	es, _ := x["eventSource"].(map[string]any)
	sens, _ := x["sensor"].(map[string]any)
	srcName, _ := es["name"].(string)
	src := ev.source(srcName)
	if meta, ok := es["metadata"].(map[string]any); ok {
		m := src["metadata"].(map[string]any)
		for k, v := range meta {
			m[k] = v
		}
	}
	typ, _ := es["type"].(string)
	event, _ := es["event"].(string)
	spec := src["spec"].(map[string]any)
	delete(spec, "service")
	if rest, ok := es["spec"].(map[string]any); ok {
		for k, v := range rest {
			spec[k] = ynode.DeepCopy(v)
		}
	}
	entries, _ := spec[typ].(map[string]any)
	if entries == nil {
		entries = map[string]any{}
		spec[typ] = entries
	}
	entries[event] = ynode.DeepCopy(es["entry"])
	sensor := ev.sensorFor(sens)
	dep, _ := x["dependency"].(map[string]any)
	if !ev.hasDependency(sensor, dep) {
		appendSpec(sensor, "dependencies", ynode.DeepCopy(dep))
	}
	trig, _ := x["trigger"].(map[string]any)
	tn, _ := trigName(trig)
	if !ev.hasTrigger(sensor, tn) {
		appendSpec(sensor, "triggers", ynode.DeepCopy(trig))
	}
}

func trigName(trig map[string]any) (string, bool) {
	t, _ := trig["template"].(map[string]any)
	n, ok := t["name"].(string)
	return n, ok
}

func (ev *eventDocs) hasTrigger(sensor docMap, name string) bool {
	l, _ := sensor["spec"].(map[string]any)["triggers"].([]any)
	for _, t := range l {
		if n, ok := trigName(t.(map[string]any)); ok && n == name && name != "" {
			return true
		}
	}
	return false
}

// sensorTrigger writes the Sensor trigger of an api trigger that an Argo file held, with its dependencies.
func (e *emitter) sensorTrigger(path string, x map[string]any) {
	ev := e.events
	sens, _ := x["sensor"].(map[string]any)
	sensor := ev.sensorFor(sens)
	if dep, ok := x["dependency"].(map[string]any); ok {
		appendSpec(sensor, "dependencies", ynode.DeepCopy(dep))
	}
	if deps, ok := x["dependencies"].(map[string]any); ok {
		for _, n := range sortedKeys(deps) {
			appendSpec(sensor, "dependencies", ynode.DeepCopy(deps[n]))
		}
	}
	trig, _ := x["trigger"].(map[string]any)
	if trig == nil {
		e.lose(path, caps.TriggerAPI, "the Ext holds no Sensor trigger; nothing is written")
		return
	}
	if tn, _ := trigName(trig); !ev.hasTrigger(sensor, tn) {
		appendSpec(sensor, "triggers", ynode.DeepCopy(trig))
	}
}

// flushEvents turns the collected documents into files.
func (e *emitter) flushEvents() {
	ev := e.events
	for _, name := range ev.cronOrder {
		c := ev.crons[name]
		idx := make([]int, 0, len(c.schedules))
		for i := range c.schedules {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		if len(idx) == 0 {
			continue
		}
		var sched []any
		for _, i := range idx {
			sched = append(sched, c.schedules[i])
		}
		spec := c.doc["spec"].(map[string]any)
		spec["schedules"] = sched
		delete(spec, "schedule")
		if n := e.node(c.path, "CronWorkflow "+name, map[string]any(c.doc)); n != nil {
			e.add(cronDir+name+".yaml", &outDoc{root: n}, c.path)
			e.made[kindCron+" "+name] = true
		}
	}
	e.writeGroup(eventsDir+"eventsource.yaml", kindEventSource, ev.sourceOrder, ev.sources)
	e.writeGroup(eventsDir+"sensor.yaml", kindSensor, ev.sensorOrder, ev.sensors)
	if len(ev.sourceOrder)+len(ev.sensorOrder) > 0 || e.needsSA {
		e.writeRBAC()
	}
}

func (e *emitter) writeGroup(file, kind string, order []string, docs map[string]docMap) {
	for _, name := range order {
		d := docs[name]
		n := e.node("pipeline/", kind+" "+name, map[string]any(d))
		if n == nil {
			continue
		}
		out := &outDoc{root: n}
		e.files[file] = append(e.files[file], out)
		e.made[kind+" "+name] = true
	}
}

func (e *emitter) writeRBAC() {
	for _, f := range rbacFiles {
		if f.account == sensorSA && len(e.events.sensorOrder) == 0 {
			continue
		}
		for _, o := range f.objects() {
			n := e.node("pipeline/", "RBAC", map[string]any(o))
			if n != nil {
				e.files[rbacDir+f.file] = append(e.files[rbacDir+f.file], &outDoc{root: n, head: []string{rbacMarker}})
			}
		}
	}
}

// sensorFor finds the Sensor that an Argo file held, with its metadata and spec keys beside dependencies and triggers.
func (ev *eventDocs) sensorFor(sens map[string]any) docMap {
	name, _ := sens["name"].(string)
	if name == "" {
		name = sensorName
	}
	_, had := ev.sensors[name]
	sensor := ev.sensor(name)
	if had {
		return sensor
	}
	delete(sensor["spec"].(map[string]any), "template")
	if meta, ok := sens["metadata"].(map[string]any); ok {
		m := sensor["metadata"].(map[string]any)
		for k, v := range meta {
			m[k] = ynode.DeepCopy(v)
		}
	}
	if rest, ok := sens["spec"].(map[string]any); ok {
		sp := sensor["spec"].(map[string]any)
		for k, v := range rest {
			sp[k] = ynode.DeepCopy(v)
		}
	}
	return sensor
}

func (ev *eventDocs) hasDependency(sensor docMap, dep map[string]any) bool {
	l, _ := sensor["spec"].(map[string]any)["dependencies"].([]any)
	for _, d := range l {
		if m, ok := d.(map[string]any); ok && m["name"] == dep["name"] {
			return true
		}
	}
	return false
}
