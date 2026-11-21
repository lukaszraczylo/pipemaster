package tekton

import (
	"encoding/json"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// listener is one EventListener under construction.
type listener struct {
	doc     *outDoc
	meta    map[string]any
	spec    map[string]any
	name    string
	sa      string
	entries []*yaml.Node
}

// listenerFor finds or starts the EventListener with the name.
func (e *emitter) listenerFor(name string) *listener {
	for _, l := range e.listeners {
		if l.name == name {
			return l
		}
	}
	l := &listener{name: name, doc: &outDoc{}}
	e.listeners = append(e.listeners, l)
	return l
}

// sourceGroup is the IR triggers that one EventListener trigger of a Tekton file produced.
type sourceGroup struct {
	x     map[string]any
	paths []string
	kinds []ir.TriggerKind
}

func sourceKey(x map[string]any) string {
	l, _ := x["listener"].(string)
	n, _ := x["name"].(string)
	r, _ := x["triggerRef"].(string)
	return l + "\x00" + n + "\x00" + r
}

// triggers writes the EventListeners, the bindings, the templates and the CronJobs of every pipeline.
func (e *emitter) triggers() error {
	var crons, bindings, templates []*outDoc
	for _, pp := range e.plans {
		b, t, err := e.pipelineTriggers(pp, &crons)
		if err != nil {
			return err
		}
		if b != nil {
			bindings = append(bindings, b)
		}
		if t != nil {
			templates = append(templates, t)
		}
	}
	for _, l := range e.listeners {
		if err := e.listenerDoc(l); err != nil {
			return err
		}
	}
	e.flushPendingListeners()
	for _, d := range bindings {
		e.add(triggersDir+"triggerbinding.yaml", d)
	}
	for _, d := range templates {
		e.add(triggersDir+"triggertemplate.yaml", d)
	}
	for _, d := range crons {
		e.add(cronDir+cronFile(d), d)
	}
	if e.generatedListener {
		e.rbac()
	}
	return nil
}

// listenerDoc fills the EventListener document of l.
func (e *emitter) listenerDoc(l *listener) error {
	el := newMap()
	el.setStr("apiVersion", apiTriggersV1)
	el.setStr("kind", kindEventListener)
	meta := newMap()
	meta.setStr("name", l.name)
	if err := meta.addExtra(l.meta, nil); err != nil {
		return err
	}
	el.set("metadata", meta.n)
	spec := newMap()
	if l.sa != "" {
		spec.setStr("serviceAccountName", l.sa)
	}
	entries := append([]*yaml.Node(nil), l.entries...)
	for _, d := range e.pendingEL[l.name] {
		if raw, ok := d.body["spec"].(map[string]any); ok {
			if list, ok := raw["triggers"].([]any); ok {
				for _, t := range list {
					n, err := plainNode(t)
					if err != nil {
						return err
					}
					entries = append(entries, n)
				}
			}
		}
	}
	delete(e.pendingEL, l.name)
	spec.set("triggers", seqOf(entries...))
	if err := spec.addExtra(l.spec, map[string]bool{"triggers": true}); err != nil {
		return err
	}
	el.set("spec", spec.n)
	l.doc.root = el.n
	e.add(triggersDir+"eventlistener.yaml", l.doc)
	return nil
}

// flushPendingListeners writes the EventListeners that no pipeline trigger rebuilt, as the Tekton file had them.
func (e *emitter) flushPendingListeners() {
	for _, name := range sortedKeys(e.pendingEL) {
		for _, d := range e.pendingEL[name] {
			e.writeResource(d)
		}
	}
	e.pendingEL = nil
}

// pipelineTriggers writes the EventListener entries, the binding, the template and the CronJobs of one pipeline.
func (e *emitter) pipelineTriggers(pp *plan, crons *[]*outDoc) (binding, template *outDoc, err error) {
	var ctxs []string
	for _, c := range ctxOrder {
		if p, _ := exprtekton.CtxParam(strings.Split(c, ".")); pp.ctx[p] {
			ctxs = append(ctxs, c)
		}
	}
	tname, bname := pp.name+templateNameSuffix, pp.name+bindingNameSuffix
	tname, bname = fit(tname, tname, maxName), fit(bname, bname, maxName)
	used := false
	sched := 0
	e.keptEntries = map[string]bool{}
	for _, t := range pp.pl.Triggers {
		if x := extOf(t.Ext); e.own && x["listener"] != nil {
			if n, ok := x["name"].(string); ok {
				e.keptEntries[n] = true
			}
		}
	}
	groups := map[string]*sourceGroup{}
	var order []string
	for i, t := range pp.pl.Triggers {
		path := pp.path + "/trigger/" + strconv.Itoa(i)
		e.foreignExt(path, t.Ext)
		x := map[string]any{}
		if e.own {
			x = extOf(t.Ext)
		}
		if t.Kind == ir.TriggerManual {
			e.note(path, caps.TriggerManual, caps.External, "tkn pipeline start "+pp.name, "a person starts the run with `tkn pipeline start "+pp.name+"`")
			continue
		}
		switch {
		case t.Kind == ir.TriggerSchedule && x["cronjob"] != nil:
			e.sourceCron(pp, path, t, x, crons)
			continue
		case x["listener"] != nil && t.Kind != ir.TriggerSchedule:
			k := sourceKey(x)
			g := groups[k]
			if g == nil {
				g = &sourceGroup{x: x}
				groups[k] = g
				order = append(order, k)
			}
			g.paths = append(g.paths, path)
			g.kinds = append(g.kinds, t.Kind)
			if t.Kind == ir.TriggerAPI {
				e.emulate(path, caps.TriggerAPI, "EventListener entry of the Tekton file", "the entry is written as the Tekton file had it")
			}
			continue
		}
		l := e.listenerFor(kindEventListenerName)
		l.sa = triggersAccount
		e.generatedListener = true
		e.owners[path] = l.doc
		var entry *yaml.Node
		switch t.Kind {
		case ir.TriggerPush, ir.TriggerTag, ir.TriggerPR:
			entry, err = e.eventTrigger(pp, i, path, t, ctxs, bname, tname)
		case ir.TriggerSchedule:
			var cj *outDoc
			entry, cj, err = e.scheduleTrigger(pp, sched, path, t, ctxs, bname, tname)
			sched++
			if cj != nil {
				*crons = append(*crons, cj)
			}
		default:
			if t.Kind == ir.TriggerAPI {
				e.lose(path, caps.TriggerAPI, "Tekton has no API trigger kind; only an EventListener entry of a Tekton file is kept")
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if entry != nil {
			used = true
			l.entries = append(l.entries, entry)
		}
	}
	for _, k := range order {
		if err := e.sourceEntry(pp, groups[k]); err != nil {
			return nil, nil, err
		}
	}
	e.requiredInputs(pp, used, groups)
	if !used {
		return nil, nil, nil
	}
	if !e.made["TriggerBinding "+bname] {
		binding = e.bindingDoc(bname, ctxs)
	}
	if !e.made["TriggerTemplate "+tname] {
		template = e.templateDoc(pp, tname, ctxs)
	}
	return binding, template, nil
}

// requiredInputs reports each input that the generated run cannot supply: the template passes no input, so a
// required input without a default fails the run.
func (e *emitter) requiredInputs(pp *plan, generated bool, _ map[string]*sourceGroup) {
	if !generated {
		return
	}
	for i, t := range pp.pl.Triggers {
		path := pp.path + "/trigger/" + strconv.Itoa(i)
		f, lv := kindFeature[t.Kind], caps.External
		if t.Kind == ir.TriggerSchedule {
			f, lv = caps.TriggerSchedule, caps.Emulated
		}
		if f == "" || (e.own && extOf(t.Ext)["listener"] != nil) {
			continue
		}
		for _, in := range pp.pl.Inputs {
			if in.Required && in.Default == "" {
				e.note(path, f, lv, "trigger without inputs",
					"the generated PipelineRun passes no value for the required input "+in.Name+", so a run fails until the TriggerTemplate or the run supplies it")
			}
		}
	}
}

// sourceCron writes the CronJob that a Tekton file held, once per name.
func (e *emitter) sourceCron(pp *plan, path string, t ir.Trigger, x map[string]any, crons *[]*outDoc) {
	cj, _ := x["cronjob"].(map[string]any)
	name, _ := cj["name"].(string)
	e.owners[path] = pp.doc
	if e.made["CronJob "+name] {
		return
	}
	e.made["CronJob "+name] = true
	meta := newMap()
	meta.setStr("name", name)
	md, _ := cj["metadata"].(map[string]any)
	_ = meta.addExtra(md, nil)
	spec := newMap()
	spec.setStr("schedule", t.Cron)
	sp, _ := cj["spec"].(map[string]any)
	if err := spec.addExtra(sp, map[string]bool{"schedule": true}); err != nil {
		e.lose(path, caps.ExtPassthrough, "the CronJob spec cannot be written: "+err.Error())
		return
	}
	root := newMap()
	root.setStr("apiVersion", apiBatchV1)
	root.setStr("kind", kindCronJob)
	root.set("metadata", meta.n)
	root.set("spec", spec.n)
	e.emulate(path, caps.TriggerSchedule, "CronJob of the Tekton file", "the CronJob is written as the Tekton file had it, with the schedule of the trigger")
	*crons = append(*crons, &outDoc{root: root.n})
}

// isGitHubInterceptor reads an interceptor kept in Ext.
func isGitHubInterceptor(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if ref, ok := m["ref"].(map[string]any); ok {
		return ref["name"] == interceptorGitHub
	}
	_, ok = m[interceptorGitHub]
	return ok && len(m) == 1
}

// sourceEntry writes the EventListener trigger that a Tekton file held, with the interceptors, bindings, template
// and service account it had. The CEL filter and overlays that the parser kept come back as written.
func (e *emitter) sourceEntry(pp *plan, g *sourceGroup) error {
	x := g.x
	lname, _ := x["listener"].(string)
	l := e.listenerFor(lname)
	for _, p := range g.paths {
		e.owners[p] = l.doc
	}
	if md, ok := x["listenerMetadata"].(map[string]any); ok && l.meta == nil {
		l.meta = md
	}
	if sp, ok := x["listenerSpec"].(map[string]any); ok && l.spec == nil {
		l.spec = sp
	}
	body := newMap()
	if n, ok := x["name"].(string); ok {
		body.setStr("name", n)
	}
	sa, _ := x["serviceAccountName"].(string)
	if sa != "" && x[extSAScope] == saScopeSpec {
		l.sa = sa
		sa = ""
	}
	target := body
	ref, hasRef := x["triggerRef"].(string)
	var trigDoc *wmap
	if hasRef {
		body.setStr("triggerRef", ref)
		target = newMap()
		trigDoc = target
	}
	if err := e.fillSourceTrigger(pp, g, target, sa); err != nil {
		return err
	}
	if hasRef {
		root := newMap()
		root.setStr("apiVersion", apiTriggersV1)
		root.setStr("kind", kindTrigger)
		meta := newMap()
		meta.setStr("name", ref)
		if md, ok := x["triggerMetadata"].(map[string]any); ok {
			if err := meta.addExtra(md, nil); err != nil {
				return err
			}
		}
		root.set("metadata", meta.n)
		root.set("spec", trigDoc.n)
		e.add(triggersDir+"trigger.yaml", &outDoc{root: root.n})
	}
	l.entries = append(l.entries, body.n)
	return nil
}

func (e *emitter) fillSourceTrigger(pp *plan, g *sourceGroup, m *wmap, sa string) error {
	x := g.x
	var ics []*yaml.Node
	if kept, ok := x["interceptors"].([]any); ok {
		for _, k := range kept {
			n, err := plainNode(k)
			if err != nil {
				e.lose(g.paths[0], caps.ExtPassthrough, "an interceptor of the Tekton file cannot be written: "+err.Error())
				continue
			}
			ics = append(ics, n)
		}
	} else {
		ics = e.generatedInterceptors(pp, g)
	}
	if len(ics) > 0 {
		m.set("interceptors", seqOf(ics...))
	}
	if b, ok := x["bindings"]; ok {
		n, err := plainNode(b)
		if err != nil {
			return err
		}
		m.set("bindings", n)
	}
	if t, ok := x["template"].(string); ok {
		m.set("template", nameOnlyRef(t))
	}
	if sa != "" {
		m.setStr("serviceAccountName", sa)
	}
	return nil
}

func dedupeStrings(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// celInterceptor writes the cel interceptor of a filter that the parser kept as opaque Tekton text.
func (e *emitter) celInterceptor(pp *plan, g *sourceGroup, filter string, overlays any) (*yaml.Node, error) {
	ex, err := expr.ParseCanonical(filter)
	o, ok := ex.(expr.Opaque)
	if err != nil || !ok || o.Dialect != dialect {
		e.lose(g.paths[0], caps.ExprOpaque, "the CEL filter of the Tekton file cannot be read back: "+filter)
		return nil, nil
	}
	var body struct {
		CEL string `json:"cel"`
	}
	if err := json.Unmarshal([]byte(o.Text), &body); err != nil {
		e.lose(g.paths[0], caps.ExprOpaque, "the CEL filter of the Tekton file cannot be read back: "+o.Text)
		return nil, nil
	}
	cel := newMap()
	cel.set("ref", nameOnly(interceptorCEL))
	ps := seqOf(nameValue(paramFilter, body.CEL))
	if overlays != nil {
		n, err := plainNode(overlays)
		if err != nil {
			e.lose(g.paths[0], caps.ExtPassthrough, "the CEL overlays of the Tekton file cannot be written: "+err.Error())
		} else {
			ps.Content = append(ps.Content, nameNode(paramOverlays, n))
		}
	}
	cel.set("params", ps)
	_ = pp
	return cel.n, nil
}

// generatedInterceptors writes the github and cel interceptors of a source trigger that kept no interceptor list.
func (e *emitter) generatedInterceptors(pp *plan, g *sourceGroup) []*yaml.Node {
	var ics []*yaml.Node
	var types []string
	for _, k := range g.kinds {
		switch k {
		case ir.TriggerPush, ir.TriggerTag:
			types = append(types, eventPush)
		case ir.TriggerPR:
			types = append(types, eventPullRequest)
		}
	}
	if types = dedupeStrings(types); len(types) > 0 {
		gh := newMap()
		gh.set("ref", nameOnly(interceptorGitHub))
		gh.set("params", seqOf(nameNode(paramEventTypes, strSeq(types))))
		ics = append(ics, gh.n)
	}
	if f, ok := g.x["filter"].(string); ok {
		if node, err := e.celInterceptor(pp, g, f, g.x[extOverlays]); err == nil && node != nil {
			ics = append(ics, node)
		}
	}
	return ics
}
