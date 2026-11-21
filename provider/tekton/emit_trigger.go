package tekton

import (
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprtekton "github.com/lukaszraczylo/pipemaster/expr/tekton"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	apiRBAC           = "rbac.authorization.k8s.io/v1"
	triggersAccount   = "pipemaster-triggers"
	listenerNamespace = "default"
	listenerPort      = "8080"
	scheduleHeader    = "X-Pipemaster-Schedule"
	curlImage         = "curlimages/curl:8.11.1"
	workspaceStorage  = "1Gi"
	refHeads          = "refs/heads/"
	refTags           = "refs/tags/"
	prActionsExpr     = "body.action in ['opened','synchronize','reopened']"
	changedFilesExpr  = "(extensions.changed_files.added + extensions.changed_files.modified + extensions.changed_files.removed)"
	utcZone           = "Etc/UTC"
	cronContainerName = "trigger"
	// generatedMarker tags a document that the emitter writes whole; the parser drops it when nobody edited it.
	generatedMarker    = "# pipemaster:generated"
	templateNameSuffix = "-run"
	bindingNameSuffix  = "-github"
)

// filterArg is what the filter marker carries: the IR trigger the CEL text was made from.
type filterArg struct {
	Kind     ir.TriggerKind `json:"kind"`
	Name     string         `json:"name,omitempty"`
	Branches ir.Filter      `json:"branches"`
	Tags     ir.Filter      `json:"tags"`
	Paths    ir.Filter      `json:"paths"`
}

// globRegex turns a glob into an anchored RE2 text. It admits only `*` (not across `/`), `**`, `?` and literals;
// ok is false for anything that CEL `matches` cannot state exactly.
func globRegex(g string) (string, bool) {
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; {
		case c == '*' && strings.HasPrefix(g[i:], "**/"):
			sb.WriteString("(.*/)?")
			i += 2
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			sb.WriteString(".*")
			i++
		case c == '*':
			sb.WriteString("[^/]*")
		case c == '?':
			sb.WriteString("[^/]")
		case strings.ContainsRune("[]{}!\\'", rune(c)):
			return "", false
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	return sb.String(), true
}

func rawRegex(g string) (string, bool) {
	r, ok := globRegex(g)
	return "r'" + r + "'", ok
}

// matchAny is `(t.matches(a) || t.matches(b))`; ok is false when a glob has no regex.
func matchAny(target string, globs []string) (string, bool) {
	var parts []string
	for _, g := range globs {
		r, ok := rawRegex(g)
		if !ok {
			return "", false
		}
		parts = append(parts, target+".matches("+r+")")
	}
	return "(" + strings.Join(parts, " || ") + ")", true
}

// filterTarget is a ref text with the ref prefix removed.
func trimRef(prefix string) string { return "body.ref.replace('" + prefix + "','',1)" }

// celFilter writes the CEL filter of a trigger. ok is false when a glob has no exact regex.
func celFilter(a filterArg) (string, bool) {
	var conds []string
	branch := func(target string) bool {
		return addFilter(&conds, target, a.Branches)
	}
	switch a.Kind {
	case ir.TriggerPush:
		conds = append(conds, "body.ref.startsWith('"+refHeads+"')")
		if !branch(trimRef(refHeads)) {
			return "", false
		}
	case ir.TriggerTag:
		conds = append(conds, "body.ref.startsWith('"+refTags+"')")
		if !addFilter(&conds, trimRef(refTags), a.Tags) {
			return "", false
		}
	case ir.TriggerPR:
		conds = append(conds, prActionsExpr)
		if !branch("body.pull_request.base.ref") {
			return "", false
		}
	case ir.TriggerSchedule:
		return "header.canonical('" + scheduleHeader + "') == '" + a.Name + "'", true
	}
	if len(a.Paths.Include)+len(a.Paths.Exclude) > 0 {
		c, ok := pathsCond(a.Paths)
		if !ok {
			return "", false
		}
		conds = append(conds, c)
	}
	return strings.Join(conds, " && "), true
}

func addFilter(conds *[]string, target string, f ir.Filter) bool {
	if len(f.Include) > 0 {
		c, ok := matchAny(target, f.Include)
		if !ok {
			return false
		}
		*conds = append(*conds, c)
	}
	for _, g := range f.Exclude {
		c, ok := matchAny(target, []string{g})
		if !ok {
			return false
		}
		*conds = append(*conds, "!"+c)
	}
	return true
}

func pathsCond(f ir.Filter) (string, bool) {
	var parts []string
	if len(f.Include) > 0 {
		c, ok := matchAny("f", f.Include)
		if !ok {
			return "", false
		}
		parts = append(parts, c)
	}
	if len(f.Exclude) > 0 {
		c, ok := matchAny("f", f.Exclude)
		if !ok {
			return "", false
		}
		parts = append(parts, "!"+c)
	}
	return changedFilesExpr + ".exists(f, " + strings.Join(parts, " && ") + ")", true
}

var kindFeature = map[ir.TriggerKind]caps.Feature{ir.TriggerPush: caps.TriggerPush, ir.TriggerTag: caps.TriggerTag, ir.TriggerPR: caps.TriggerPullRequest}

// overlayKey is the extensions key that carries a ctx param.
func overlayKey(param string) string { return strings.ReplaceAll(param, "-", "_") }

// ctxOverlay is the CEL expression that computes a context value for one trigger kind.
func ctxOverlay(kind ir.TriggerKind, ctx string) string {
	q := expr.QuoteSingle
	push := kind == ir.TriggerPush
	tag := kind == ir.TriggerTag
	pr := kind == ir.TriggerPR
	switch ctx {
	case expr.CtxEvent:
		switch kind {
		case ir.TriggerPR:
			return q("pull_request")
		case ir.TriggerSchedule:
			return q("schedule")
		}
		return q("push")
	case expr.CtxBranch:
		if push {
			return trimRef(refHeads)
		}
	case expr.CtxTag:
		if tag {
			return trimRef(refTags)
		}
	case expr.CtxRefName:
		switch {
		case push:
			return trimRef(refHeads)
		case tag:
			return trimRef(refTags)
		case pr:
			return "string(int(body.number)) + '/merge'"
		}
	case expr.CtxRefType:
		switch {
		case tag:
			return q("tag")
		case push || pr:
			return q("branch")
		}
	case expr.CtxRef:
		switch {
		case push || tag:
			return "body.ref"
		case pr:
			return "'refs/pull/' + string(int(body.number)) + '/merge'"
		}
	case expr.CtxSHA:
		switch {
		case push || tag:
			return "body.after"
		case pr:
			return "body.pull_request.head.sha"
		}
	case expr.CtxRepo:
		if push || tag || pr {
			return "body.repository.full_name"
		}
	case expr.CtxActor:
		if push || tag || pr {
			return "body.sender.login"
		}
	case expr.CtxPRNumber:
		if pr {
			return "string(int(body.number))"
		}
	case expr.CtxPRHeadRef:
		if pr {
			return "body.pull_request.head.ref"
		}
	case expr.CtxPRBaseRef:
		if pr {
			return "body.pull_request.base.ref"
		}
	}
	return "''"
}

func (e *emitter) triggers() error {
	if len(e.plans) == 0 {
		return nil
	}
	el := newMap()
	el.setStr("apiVersion", apiTriggersV1)
	el.setStr("kind", kindEventListener)
	meta := newMap()
	meta.setStr("name", kindEventListenerName)
	el.set("metadata", meta.n)
	var entries []*yaml.Node
	var crons, bindings, templates []*outDoc
	d := &outDoc{}
	for _, pp := range e.plans {
		b, t, err := e.pipelineTriggers(pp, d, &entries, &crons)
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
	if len(entries) == 0 && len(crons) == 0 {
		return nil
	}
	spec := newMap()
	spec.setStr("serviceAccountName", triggersAccount)
	spec.set("triggers", seqOf(entries...))
	el.set("spec", spec.n)
	d.root = el.n
	e.add(triggersDir+"eventlistener.yaml", d)
	for _, d := range bindings {
		e.add(triggersDir+"triggerbinding.yaml", d)
	}
	for _, d := range templates {
		e.add(triggersDir+"triggertemplate.yaml", d)
	}
	for _, d := range crons {
		e.add(cronDir+cronFile(d), d)
	}
	e.rbac()
	return nil
}

func cronFile(d *outDoc) string {
	for i := 0; i+1 < len(d.root.Content); i += 2 {
		if d.root.Content[i].Value == "metadata" {
			m := d.root.Content[i+1]
			for j := 0; j+1 < len(m.Content); j += 2 {
				if m.Content[j].Value == "name" {
					return m.Content[j+1].Value + ".yaml"
				}
			}
		}
	}
	return "cron.yaml"
}

// pipelineTriggers writes the EventListener entries, the binding, the template and the CronJobs of one pipeline.
func (e *emitter) pipelineTriggers(pp *plan, listener *outDoc, entries *[]*yaml.Node, crons *[]*outDoc) (binding, template *outDoc, err error) {
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
	for i, t := range pp.pl.Triggers {
		path := pp.path + "/trigger/" + strconv.Itoa(i)
		e.owners[path] = listener
		e.foreignExt(path, t.Ext)
		if t.Kind == ir.TriggerManual {
			e.note(path, caps.TriggerManual, caps.External, "tkn pipeline start "+pp.name, "a person starts the run with `tkn pipeline start "+pp.name+"`")
			continue
		}
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
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if entry != nil {
			used = true
			*entries = append(*entries, entry)
		}
	}
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

func (e *emitter) eventTrigger(pp *plan, i int, path string, t ir.Trigger, ctxs []string, bname, tname string) (*yaml.Node, error) {
	tn := triggerName(pp, t.Kind, i)
	arg := filterArg{Kind: t.Kind, Branches: t.Branches, Tags: t.Tags, Paths: t.Paths, Name: tn}
	filter, ok := celFilter(arg)
	if !ok {
		filter = "false"
		for _, f := range []struct {
			f  caps.Feature
			ok bool
		}{{caps.TriggerFilterBranch, len(t.Branches.Include)+len(t.Branches.Exclude) > 0}, {caps.TriggerFilterTags, len(t.Tags.Include)+len(t.Tags.Exclude) > 0}, {caps.TriggerFilterPaths, len(t.Paths.Include)+len(t.Paths.Exclude) > 0}} {
			if f.ok {
				e.lose(path, f.f, "a glob has no exact CEL regex, so the filter is false and the trigger never fires; the globs stay in the marker")
			}
		}
	}
	if err := e.markArg(kindFeature[t.Kind], path, arg); err != nil {
		return nil, err
	}
	gh := []*yaml.Node{nameValue("eventTypes", "")}
	types := str("push")
	if t.Kind == ir.TriggerPR {
		types = str("pull_request")
	}
	gh[0] = nameNode("eventTypes", seqOf(types))
	if len(t.Paths.Include)+len(t.Paths.Exclude) > 0 {
		en := newMap()
		en.set("enabled", boolNode(true))
		gh = append(gh, nameNode("addChangedFiles", en.n))
	}
	return e.triggerEntry(tn, t.Kind, filter, gh, ctxs, bname, tname), nil
}

func nameNode(name string, v *yaml.Node) *yaml.Node {
	m := newMap()
	m.setStr("name", name)
	m.set("value", v)
	return m.n
}

func triggerName(pp *plan, k ir.TriggerKind, i int) string {
	n := pp.name + "-" + string(k) + "-" + strconv.Itoa(i)
	return fit(n, n, maxName)
}

// triggerEntry writes one EventListener trigger: the github interceptor, the CEL interceptor with the context overlays, the binding and the template.
func (e *emitter) triggerEntry(name string, kind ir.TriggerKind, filter string, github []*yaml.Node, ctxs []string, bname, tname string) *yaml.Node {
	var ics []*yaml.Node
	if github != nil {
		g := newMap()
		g.set("ref", nameOnly(interceptorGitHub))
		g.set("params", seqOf(github...))
		ics = append(ics, g.n)
	}
	overlays := seqOf()
	for _, c := range ctxs {
		p, _ := exprtekton.CtxParam(strings.Split(c, "."))
		o := newMap()
		o.setStr("key", overlayKey(p))
		o.setStr("expression", ctxOverlay(kind, c))
		overlays.Content = append(overlays.Content, o.n)
	}
	cel := newMap()
	cel.set("ref", nameOnly(interceptorCEL))
	ps := seqOf(nameValue(paramFilter, filter))
	if len(overlays.Content) > 0 {
		ps.Content = append(ps.Content, nameNode("overlays", overlays))
	}
	cel.set("params", ps)
	ics = append(ics, cel.n)
	m := newMap()
	m.setStr("name", name)
	m.set("interceptors", seqOf(ics...))
	m.set("bindings", seqOf(nameOnlyRef(bname)))
	tpl := newMap()
	tpl.setStr("ref", tname)
	m.set("template", tpl.n)
	return m.n
}

func nameOnly(n string) *yaml.Node {
	m := newMap()
	m.setStr("name", n)
	return m.n
}

func nameOnlyRef(n string) *yaml.Node {
	m := newMap()
	m.setStr("ref", n)
	return m.n
}

func (e *emitter) bindingDoc(name string, ctxs []string) *outDoc {
	root := newMap()
	root.setStr("apiVersion", apiTriggersV1)
	root.setStr("kind", "TriggerBinding")
	meta := newMap()
	meta.setStr("name", name)
	root.set("metadata", meta.n)
	ps := seqOf()
	for _, c := range ctxs {
		p, _ := exprtekton.CtxParam(strings.Split(c, "."))
		ps.Content = append(ps.Content, nameValue(p, "$(extensions."+overlayKey(p)+")"))
	}
	spec := newMap()
	spec.set("params", ps)
	root.set("spec", spec.n)
	return &outDoc{root: root.n, head: []string{generatedMarker}}
}

func (e *emitter) templateDoc(pp *plan, name string, ctxs []string) *outDoc {
	root := newMap()
	root.setStr("apiVersion", apiTriggersV1)
	root.setStr("kind", kindTriggerTemplate)
	meta := newMap()
	meta.setStr("name", name)
	root.set("metadata", meta.n)
	tps := seqOf()
	rps := seqOf()
	for _, c := range ctxs {
		p, _ := exprtekton.CtxParam(strings.Split(c, "."))
		m := newMap()
		m.setStr("name", p)
		m.setStr("default", "")
		tps.Content = append(tps.Content, m.n)
		rps.Content = append(rps.Content, nameValue(p, "$(tt.params."+p+")"))
	}
	run := newMap()
	run.setStr("apiVersion", apiPipelinesV1)
	run.setStr("kind", kindPipelineRun)
	rm := newMap()
	rm.setStr("generateName", pp.name+"-run-")
	run.set("metadata", rm.n)
	rs := newMap()
	rs.set("pipelineRef", nameOnly(pp.name))
	rs.set("params", rps)
	if pp.ws {
		vc := newMap()
		sp := newMap()
		sp.set("accessModes", strSeq([]string{"ReadWriteOnce"}))
		req := newMap()
		st := newMap()
		st.setStr("storage", workspaceStorage)
		req.set("requests", st.n)
		sp.set("resources", req.n)
		vc.set("spec", sp.n)
		w := newMap()
		w.setStr("name", workspaceName)
		w.set("volumeClaimTemplate", vc.n)
		rs.set("workspaces", seqOf(w.n))
	}
	run.set("spec", rs.n)
	spec := newMap()
	spec.set("params", tps)
	spec.set("resourcetemplates", seqOf(run.n))
	root.set("spec", spec.n)
	return &outDoc{root: root.n, head: []string{generatedMarker}}
}

// scheduleTrigger writes the CronJob that posts to the EventListener and the listener trigger that takes the post.
func (e *emitter) scheduleTrigger(pp *plan, n int, path string, t ir.Trigger, ctxs []string, bname, tname string) (*yaml.Node, *outDoc, error) {
	tag := pp.name + "/" + strconv.Itoa(n)
	filter, _ := celFilter(filterArg{Kind: ir.TriggerSchedule, Name: tag})
	jname := pp.name + "-schedule-" + strconv.Itoa(n)
	jname = fit(jname, jname, cronNameMax)
	e.emulate(path, caps.TriggerSchedule, "CronJob posting to the EventListener",
		"CronJob "+jname+" posts to el-"+kindEventListenerName+" in namespace "+listenerNamespace+"; deploy both in that namespace, or edit the host. The run has an empty ctx-repo because the post carries no repository")
	e.mark(caps.TriggerSchedule, path)
	entry := e.triggerEntry(jname, ir.TriggerSchedule, filter, nil, ctxs, bname, tname)
	return entry, cronJobDoc(jname, t.Cron, tag), nil
}

// cronJobDoc is the CronJob that posts the schedule tag to the EventListener.
func cronJobDoc(jname, cron, tag string) *outDoc {
	cj := newMap()
	cj.setStr("apiVersion", apiBatchV1)
	cj.setStr("kind", kindCronJob)
	meta := newMap()
	meta.setStr("name", jname)
	cj.set("metadata", meta.n)
	c := newMap()
	c.setStr("name", cronContainerName)
	c.setStr("image", curlImage)
	url := "http://el-" + kindEventListenerName + "." + listenerNamespace + ".svc.cluster.local:" + listenerPort
	c.set("args", strSeq([]string{"-sS", "--fail", "-X", "POST", "-H", "Content-Type: application/json", "-H", scheduleHeader + ": " + tag, "-d", "{}", url}))
	pod := newMap()
	pod.setStr("restartPolicy", "Never")
	pod.set("containers", seqOf(c.n))
	tpl := newMap()
	tpl.set("spec", pod.n)
	jspec := newMap()
	jspec.set("template", tpl.n)
	jt := newMap()
	jt.set("spec", jspec.n)
	spec := newMap()
	spec.setStr("schedule", cron)
	spec.setStr("timeZone", utcZone)
	spec.set("jobTemplate", jt.n)
	cj.set("spec", spec.n)
	return &outDoc{root: cj.n, head: []string{generatedMarker}}
}

// rbac writes the ServiceAccount and the roles that the EventListener needs.
func (e *emitter) rbac() {
	for _, d := range rbacDocs() {
		e.add(rbacFile, d)
	}
}

// rbacDocs lists the objects that give the EventListener its rights.
func rbacDocs() []*outDoc {
	rule := func(groups, resources, verbs []string) *yaml.Node {
		m := newMap()
		m.set("apiGroups", strSeq(groups))
		m.set("resources", strSeq(resources))
		m.set("verbs", strSeq(verbs))
		return m.n
	}
	obj := func(api, kind, name string) *wmap {
		m := newMap()
		m.setStr("apiVersion", api)
		m.setStr("kind", kind)
		md := newMap()
		md.setStr("name", name)
		if kind == "ServiceAccount" || kind == "Role" || kind == "RoleBinding" {
			md.setStr("namespace", listenerNamespace)
		}
		m.set("metadata", md.n)
		return m
	}
	bind := func(api, kind, name, roleKind string) *outDoc {
		m := obj(api, kind, name)
		sub := newMap()
		sub.setStr("kind", "ServiceAccount")
		sub.setStr("name", triggersAccount)
		sub.setStr("namespace", listenerNamespace)
		m.set("subjects", seqOf(sub.n))
		ref := newMap()
		ref.setStr("apiGroup", "rbac.authorization.k8s.io")
		ref.setStr("kind", roleKind)
		ref.setStr("name", name)
		m.set("roleRef", ref.n)
		return &outDoc{root: m.n}
	}
	read := []string{"get", "list", "watch"}
	role := obj(apiRBAC, "Role", triggersAccount)
	role.set("rules", seqOf(
		rule([]string{"triggers.tekton.dev"}, []string{"eventlisteners", "triggerbindings", "triggertemplates", "triggers", "interceptors"}, read),
		rule([]string{""}, []string{"configmaps", "secrets"}, read),
		rule([]string{"tekton.dev"}, []string{"pipelineruns", "taskruns"}, []string{"create"}),
	))
	crole := obj(apiRBAC, "ClusterRole", triggersAccount)
	crole.set("rules", seqOf(rule([]string{"triggers.tekton.dev"}, []string{"clustertriggerbindings", "clusterinterceptors", "interceptors"}, read)))
	sa := obj("v1", "ServiceAccount", triggersAccount)
	docs := []*outDoc{
		{root: sa.n}, {root: role.n}, bind(apiRBAC, "RoleBinding", triggersAccount, "Role"),
		{root: crole.n}, bind(apiRBAC, "ClusterRoleBinding", triggersAccount, "ClusterRole"),
	}
	for _, d := range docs {
		d.head = []string{generatedMarker}
	}
	return docs
}
