package tekton

import (
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
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
	prSameRepoExpr    = "body.pull_request.head.repo.full_name == body.repository.full_name"
	changedFilesExpr  = "(extensions.changed_files.added + extensions.changed_files.modified + extensions.changed_files.removed)"
	utcZone           = "Etc/UTC"
	cronContainerName = "trigger"
	githubSecretName  = "pipemaster-github-webhook"
	githubSecretKey   = "token"
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
		conds = append(conds, prActionsExpr, prSameRepoExpr)
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

func cronFile(d *outDoc) string {
	for i := 0; i+1 < len(d.Root.Content); i += 2 {
		if d.Root.Content[i].Value == "metadata" {
			m := d.Root.Content[i+1]
			for j := 0; j+1 < len(m.Content); j += 2 {
				if m.Content[j].Value == "name" {
					return m.Content[j+1].Value + ".yaml"
				}
			}
		}
	}
	return "cron.yaml"
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
		en.Set("enabled", boolNode(true))
		gh = append(gh, nameNode("addChangedFiles", en.N))
	}
	sec := newMap()
	sec.SetStr("secretName", githubSecretName)
	sec.SetStr("secretKey", githubSecretKey)
	gh = append(gh, nameNode(paramSecretRef, sec.N))
	if t.Kind == ir.TriggerPR {
		e.note(path, caps.TriggerPullRequest, caps.Emulated, "same-repository pull requests only",
			"the filter admits only pull requests from this repository, because the code of a fork would run with every secret of the Task; GitHub Actions withholds secrets from fork runs. Remove the head.repo term of the filter to accept forks")
	}
	e.note(path, kindFeature[t.Kind], caps.External, "Secret "+githubSecretName,
		"create the Secret "+githubSecretName+" with key "+githubSecretKey+" holding the GitHub webhook secret; the github interceptor rejects a webhook that it does not sign")
	return e.triggerEntry(tn, t.Kind, filter, gh, ctxs, bname, tname), nil
}

func nameNode(name string, v *yaml.Node) *yaml.Node {
	m := newMap()
	m.SetStr("name", name)
	m.Set("value", v)
	return m.N
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
		g.Set("ref", nameOnly(interceptorGitHub))
		g.Set("params", seqOf(github...))
		ics = append(ics, g.N)
	}
	overlays := seqOf()
	for _, c := range ctxs {
		p, _ := ctxparam.Param(strings.Split(c, "."))
		o := newMap()
		o.SetStr("key", overlayKey(p))
		o.SetStr("expression", ctxOverlay(kind, c))
		overlays.Content = append(overlays.Content, o.N)
	}
	cel := newMap()
	cel.Set("ref", nameOnly(interceptorCEL))
	ps := seqOf(nameValue(paramFilter, filter))
	if len(overlays.Content) > 0 {
		ps.Content = append(ps.Content, nameNode("overlays", overlays))
	}
	cel.Set("params", ps)
	ics = append(ics, cel.N)
	m := newMap()
	m.SetStr("name", name)
	m.Set("interceptors", seqOf(ics...))
	m.Set("bindings", seqOf(nameOnlyRef(bname)))
	tpl := newMap()
	tpl.SetStr("ref", tname)
	m.Set("template", tpl.N)
	return m.N
}

func nameOnly(n string) *yaml.Node {
	m := newMap()
	m.SetStr("name", n)
	return m.N
}

func nameOnlyRef(n string) *yaml.Node {
	m := newMap()
	m.SetStr("ref", n)
	return m.N
}

func (e *emitter) bindingDoc(name string, ctxs []string) *outDoc {
	root := newMap()
	root.SetStr("apiVersion", apiTriggersV1)
	root.SetStr("kind", "TriggerBinding")
	meta := newMap()
	meta.SetStr("name", name)
	root.Set("metadata", meta.N)
	ps := seqOf()
	for _, c := range ctxs {
		p, _ := ctxparam.Param(strings.Split(c, "."))
		ps.Content = append(ps.Content, nameValue(p, "$(extensions."+overlayKey(p)+")"))
	}
	spec := newMap()
	spec.Set("params", ps)
	root.Set("spec", spec.N)
	return &outDoc{Root: root.N, Head: []string{generatedMarker}}
}

func (e *emitter) templateDoc(pp *plan, name string, ctxs []string) *outDoc {
	root := newMap()
	root.SetStr("apiVersion", apiTriggersV1)
	root.SetStr("kind", kindTriggerTemplate)
	meta := newMap()
	meta.SetStr("name", name)
	root.Set("metadata", meta.N)
	tps := seqOf()
	rps := seqOf()
	for _, c := range ctxs {
		p, _ := ctxparam.Param(strings.Split(c, "."))
		m := newMap()
		m.SetStr("name", p)
		m.SetStr("default", "")
		tps.Content = append(tps.Content, m.N)
		rps.Content = append(rps.Content, nameValue(p, "$(tt.params."+p+")"))
	}
	run := newMap()
	run.SetStr("apiVersion", apiPipelinesV1)
	run.SetStr("kind", kindPipelineRun)
	rm := newMap()
	rm.SetStr("generateName", pp.name+"-run-")
	run.Set("metadata", rm.N)
	rs := newMap()
	rs.Set("pipelineRef", nameOnly(pp.name))
	rs.Set("params", rps)
	if pp.ws {
		vc := newMap()
		sp := newMap()
		sp.Set("accessModes", strSeq([]string{"ReadWriteOnce"}))
		req := newMap()
		st := newMap()
		st.SetStr("storage", workspaceStorage)
		req.Set("requests", st.N)
		sp.Set("resources", req.N)
		vc.Set("spec", sp.N)
		w := newMap()
		w.SetStr("name", workspaceName)
		w.Set("volumeClaimTemplate", vc.N)
		rs.Set("workspaces", seqOf(w.N))
	}
	run.Set("spec", rs.N)
	spec := newMap()
	spec.Set("params", tps)
	spec.Set("resourcetemplates", seqOf(run.N))
	root.Set("spec", spec.N)
	return &outDoc{Root: root.N, Head: []string{generatedMarker}}
}

// scheduleTrigger writes the CronJob that posts to the EventListener and the listener trigger that takes the post.
func (e *emitter) scheduleTrigger(pp *plan, n int, path string, t ir.Trigger, ctxs []string, bname, tname string) (*yaml.Node, *outDoc, error) {
	tag := pp.name + "/" + strconv.Itoa(n)
	filter, _ := celFilter(filterArg{Kind: ir.TriggerSchedule, Name: tag})
	jname := pp.name + "-schedule-" + strconv.Itoa(n)
	jname = fit(jname, jname, cronNameMax)
	e.emulate(path, caps.TriggerSchedule, "CronJob posting to the EventListener",
		"CronJob "+jname+" posts to el-"+scheduleListenerName+" in namespace "+listenerNamespace+"; deploy both in that namespace, or edit the host. The post has an empty body, so every ctx-* param is empty and a clone step has no repository. The entry matches only the "+scheduleHeader+" header, so any pod that reaches the service of listener "+scheduleListenerName+" can start a run. The listener has no Ingress, and the webhook listener "+kindEventListenerName+" does not take schedule entries. Do not expose "+scheduleListenerName+", and limit it with a NetworkPolicy that admits only the CronJob pods")
	e.mark(caps.TriggerSchedule, path)
	if e.keptEntries[jname] {
		return nil, cronJobDoc(jname, t.Cron, tag), nil
	}
	entry := e.triggerEntry(jname, ir.TriggerSchedule, filter, nil, ctxs, bname, tname)
	return entry, cronJobDoc(jname, t.Cron, tag), nil
}

// cronJobDoc is the CronJob that posts the schedule tag to the EventListener.
func cronJobDoc(jname, cron, tag string) *outDoc {
	cj := newMap()
	cj.SetStr("apiVersion", apiBatchV1)
	cj.SetStr("kind", kindCronJob)
	meta := newMap()
	meta.SetStr("name", jname)
	cj.Set("metadata", meta.N)
	c := newMap()
	c.SetStr("name", cronContainerName)
	c.SetStr("image", curlImage)
	url := "http://el-" + scheduleListenerName + "." + listenerNamespace + ".svc.cluster.local:" + listenerPort
	c.Set("args", strSeq([]string{"-sS", "--fail", "-X", "POST", "-H", "Content-Type: application/json", "-H", scheduleHeader + ": " + tag, "-d", "{}", url}))
	pod := newMap()
	pod.SetStr("restartPolicy", "Never")
	pod.Set("containers", seqOf(c.N))
	tpl := newMap()
	tpl.Set("spec", pod.N)
	jspec := newMap()
	jspec.Set("template", tpl.N)
	jt := newMap()
	jt.Set("spec", jspec.N)
	spec := newMap()
	spec.SetStr("schedule", cron)
	spec.SetStr("timeZone", utcZone)
	spec.Set("jobTemplate", jt.N)
	cj.Set("spec", spec.N)
	return &outDoc{Root: cj.N, Head: []string{generatedMarker}}
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
		m.Set("apiGroups", strSeq(groups))
		m.Set("resources", strSeq(resources))
		m.Set("verbs", strSeq(verbs))
		return m.N
	}
	obj := func(api, kind, name string) *wmap {
		m := newMap()
		m.SetStr("apiVersion", api)
		m.SetStr("kind", kind)
		md := newMap()
		md.SetStr("name", name)
		if kind == "ServiceAccount" || kind == "Role" || kind == "RoleBinding" {
			md.SetStr("namespace", listenerNamespace)
		}
		m.Set("metadata", md.N)
		return m
	}
	bind := func(api, kind, name, roleKind string) *outDoc {
		m := obj(api, kind, name)
		sub := newMap()
		sub.SetStr("kind", "ServiceAccount")
		sub.SetStr("name", triggersAccount)
		sub.SetStr("namespace", listenerNamespace)
		m.Set("subjects", seqOf(sub.N))
		ref := newMap()
		ref.SetStr("apiGroup", "rbac.authorization.k8s.io")
		ref.SetStr("kind", roleKind)
		ref.SetStr("name", name)
		m.Set("roleRef", ref.N)
		return &outDoc{Root: m.N}
	}
	read := []string{"get", "list", "watch"}
	role := obj(apiRBAC, "Role", triggersAccount)
	role.Set("rules", seqOf(
		rule([]string{"triggers.tekton.dev"}, []string{"eventlisteners", "triggerbindings", "triggertemplates", "triggers", "interceptors"}, read),
		rule([]string{""}, []string{"configmaps", "secrets"}, read),
		rule([]string{"tekton.dev"}, []string{"pipelineruns", "taskruns"}, []string{"create"}),
	))
	crole := obj(apiRBAC, "ClusterRole", triggersAccount)
	crole.Set("rules", seqOf(rule([]string{"triggers.tekton.dev"}, []string{"clustertriggerbindings", "clusterinterceptors", "interceptors"}, read)))
	sa := obj("v1", "ServiceAccount", triggersAccount)
	docs := []*outDoc{
		{Root: sa.N}, {Root: role.N}, bind(apiRBAC, "RoleBinding", triggersAccount, "Role"),
		{Root: crole.N}, bind(apiRBAC, "ClusterRoleBinding", triggersAccount, "ClusterRole"),
	}
	for _, d := range docs {
		d.Head = []string{generatedMarker}
	}
	return docs
}
