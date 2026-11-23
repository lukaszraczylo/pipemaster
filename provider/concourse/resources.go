package concourse

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const (
	typeGit  = "git"
	typeTime = "time"

	varOpen      = "(("
	globChars    = "*?[{"
	fullDayHours = 24
	hourMinutes  = 60
)

// resource is one entry of the resources list.
type resource struct {
	raw  any
	m    *omap
	name string
	typ  string
	line int
}

// source is the source map of the resource; nil when it has none.
func (r *resource) source() *omap {
	m, _ := asMap(valueOf(r.m, "source"))
	return m
}

func (b *pipeBuilder) readResources(r *reader) {
	v, ok := r.Take(ExtResources)
	list, _ := v.([]any)
	if !ok {
		return
	}
	for _, raw := range list {
		m, isMap := asMap(raw)
		if !isMap {
			b.resOrder = append(b.resOrder, &resource{raw: raw, line: r.Line(ExtResources)})
			continue
		}
		name, _ := text(m.Vals["name"])
		typ, _ := text(m.Vals["type"])
		res := &resource{raw: m, m: m, name: name, typ: typ, line: r.Line(ExtResources)}
		b.resOrder = append(b.resOrder, res)
		if _, dup := b.res[name]; name != "" && !dup {
			b.res[name] = res
		}
	}
}

// readTypes records the names that resource_types defines, so a type that shadows git or time is no built-in.
func (b *pipeBuilder) readTypes(r *reader) {
	list, _ := r.M.Vals[ExtResourceTypes].([]any)
	for _, raw := range list {
		m, _ := asMap(raw)
		if name, ok := text(valueOf(m, "name")); ok {
			b.types[name] = true
		}
	}
}

// builtin reports whether the resource has the built-in type t, with no resource_types entry that replaces it.
func (b *pipeBuilder) builtin(r *resource, t string) bool {
	return r != nil && r.typ == t && !b.types[t]
}

// keepResources keeps every resource verbatim and reports the keys that have no IR field.
func (b *pipeBuilder) keepResources(k *sink) {
	if len(b.resOrder) == 0 {
		return
	}
	var list []any
	for _, r := range b.resOrder {
		list = append(list, plain(r.raw))
		if r.m == nil {
			b.note(caps.ExtPassthrough, caps.Lost, k.Path, strategyExt, "a resources entry is not a mapping; it is kept in Ext", r.line)
			continue
		}
		var kept []string
		for _, key := range r.m.Order {
			if key != "name" && key != "type" {
				kept = append(kept, key)
			}
		}
		detail := fmt.Sprintf("resource %s (type %s): keys %s have no IR field; the definition is kept in Ext", r.name, r.typ, strings.Join(kept, ", "))
		if len(kept) == 0 {
			detail = fmt.Sprintf("resource %s (type %s) has no IR field; the definition is kept in Ext", r.name, r.typ)
		}
		if !b.used[r.name] {
			detail += "; the resource is unused: no job gets or puts it"
		}
		b.note(caps.ExtPassthrough, caps.Lost, k.Path, strategyExt, detail, r.line)
	}
	k.Out[ExtResources] = list
}

// sourceTrigger is the IR trigger of one resource, or an api trigger when the source cannot be read.
type sourceTrigger struct {
	uri  string
	trig ir.Trigger
	// merge is true for a push trigger that can share one trigger with another that has the same repo and paths.
	merge bool
}

// triggers builds the pipeline triggers from the resources that the jobs trigger on. The IR trigger is pipeline-wide,
// so a job whose own triggers are fewer than the union becomes manual and keeps its own in Ext, never running on a
// trigger it did not have.
func (b *pipeBuilder) triggers(pl *ir.Pipeline, jobs []*builtJob) {
	var order []string
	seen := map[string]bool{}
	for _, j := range jobs {
		for _, n := range j.roots {
			if !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}
	var out []sourceTrigger
	for _, n := range order {
		out = append(out, b.sourceTrigger(n))
	}
	pl.Triggers = b.mergePushes(out)
	for _, j := range jobs {
		if len(j.roots) == 0 || len(j.roots) == len(order) {
			continue
		}
		j.job.Manual = true
		refs := make([]any, len(j.roots))
		for i, n := range j.roots {
			refs[i] = n
		}
		if j.job.Ext == nil {
			j.job.Ext = ir.Ext{dialect: map[string]any{}}
		}
		j.job.Ext[dialect].(map[string]any)[ExtTriggers] = refs
		b.note(caps.TriggerResource, caps.Lost, b.path()+"/job/"+j.job.ID, strategyExt,
			fmt.Sprintf("job %s triggers on %s only, but an IR trigger is pipeline-wide; the job runs only on a manual start and its own triggers are kept in Ext", j.job.ID, strings.Join(j.roots, ", ")), 0)
	}
}

// mergePushes joins the push triggers of one repo and one path filter into a trigger with several branches.
func (b *pipeBuilder) mergePushes(in []sourceTrigger) []ir.Trigger {
	var out []ir.Trigger
	at := make([]int, len(in))
	for i, st := range in {
		if j := mergeTarget(in[:i], st); j >= 0 {
			at[i] = at[j]
			t := &out[at[i]]
			te := t.Ext[dialect].(map[string]any)
			for _, br := range st.trig.Branches.Include {
				if !slices.Contains(t.Branches.Include, br) {
					t.Branches.Include = append(t.Branches.Include, br)
				}
			}
			te[ExtResourcesRef] = append(te[ExtResourcesRef].([]any), st.trig.Ext[dialect].(map[string]any)[ExtResourcesRef].([]any)...)
			b.note(caps.TriggerFilterBranch, caps.Lost, b.path(), "one push trigger with several branches",
				fmt.Sprintf("resources %v are one push trigger with branches %v; Concourse runs the job once per branch event", te[ExtResourcesRef], t.Branches.Include), 0)
			continue
		}
		at[i] = len(out)
		out = append(out, st.trig)
	}
	return out
}

// mergeTarget is the index of the earlier trigger that st can join, or -1.
func mergeTarget(prev []sourceTrigger, st sourceTrigger) int {
	if !st.merge {
		return -1
	}
	for j, p := range prev {
		if p.merge && p.uri == st.uri && slices.Equal(p.trig.Paths.Include, st.trig.Paths.Include) && slices.Equal(p.trig.Paths.Exclude, st.trig.Paths.Exclude) {
			return j
		}
	}
	return -1
}

func resourceExt(names ...string) ir.Ext {
	refs := make([]any, len(names))
	for i, n := range names {
		refs[i] = n
	}
	return ir.Ext{dialect: map[string]any{ExtResourcesRef: refs}}
}

// failSafe is the api trigger for a resource trigger that the parser cannot read exactly: it never fires.
func (b *pipeBuilder) failSafe(r *resource, name, why string) sourceTrigger {
	e := resourceExt(name)
	e[dialect].(map[string]any)[ExtUnreadable] = why
	b.note(caps.TriggerAPI, caps.Lost, b.path(), strategyExt,
		fmt.Sprintf("the trigger of resource %s is unreadable (%s); it becomes an api trigger that never fires, and the resource is kept in Ext", name, why), lineOf(r))
	return sourceTrigger{trig: ir.Trigger{Kind: ir.TriggerAPI, Ext: e}}
}

func lineOf(r *resource) int {
	if r == nil {
		return 0
	}
	return r.line
}

func (b *pipeBuilder) sourceTrigger(name string) sourceTrigger {
	r := b.res[name]
	switch {
	case b.builtin(r, typeGit):
		return b.gitTrigger(r)
	case b.builtin(r, typeTime):
		return b.timeTrigger(r)
	case r == nil:
		b.note(caps.TriggerResource, caps.Lost, b.path(), strategyExt, "a get triggers on resource "+name+", which the pipeline does not define", 0)
		return sourceTrigger{trig: ir.Trigger{Kind: ir.TriggerResource, Resource: &ir.ResourceRef{Name: name}, Ext: resourceExt(name)}}
	}
	return sourceTrigger{trig: ir.Trigger{Kind: ir.TriggerResource, Resource: &ir.ResourceRef{Name: name, Type: r.typ}, Ext: resourceExt(name)}}
}

// gitInert lists the git source keys that do not change which versions the resource emits.
var gitInert = map[string]bool{
	"uri": true, "private_key": true, "private_key_user": true, "private_key_passphrase": true, "username": true,
	"password": true, "credential_hosts": true, "skip_ssl_verification": true, "submodule_credentials": true,
	"git_config": true, "https_tunnel": true, "forward_agent": true, "debug": true, "commit_verification_keys": true,
	"commit_verification_key_ids": true, "gpg_keyserver": true, "git_crypt_key": true, "sparse_paths": true,
	"fetch_tags": true, "version_depth": true, "disable_ci_skip": true,
	"github_app_id": true, "github_app_private_key": true, "github_app_installation_id": true,
}

// gitTrigger reads branch, paths, ignore_paths and tag_filter. Any other key that can filter versions, or a
// value it cannot read exactly, gives the api trigger, so the trigger is never wider than the source.
func (b *pipeBuilder) gitTrigger(r *resource) sourceTrigger {
	src := r.source()
	if src == nil {
		return b.failSafe(r, r.name, "the git resource has no source map")
	}
	var branch, tag string
	var paths, ignore []string
	// Sorted keys make the first unreadable key independent of the key order of the file.
	keys := slices.Clone(src.Order)
	slices.Sort(keys)
	for _, key := range keys {
		v := src.Vals[key]
		switch key {
		case "branch", "tag_filter":
			t, ok := text(v)
			if !ok || strings.Contains(t, varOpen) || (key == "branch" && strings.ContainsAny(t, globChars)) {
				return b.failSafe(r, r.name, "source."+key+" is not a literal")
			}
			if key == "branch" {
				branch = t
			} else {
				tag = t
			}
		case "paths", "ignore_paths":
			l, ok := stringList(v)
			if !ok || slices.ContainsFunc(l, func(s string) bool { return strings.Contains(s, varOpen) }) {
				return b.failSafe(r, r.name, "source."+key+" is not a list of literals")
			}
			if key == "paths" {
				paths = l
			} else {
				ignore = l
			}
		default:
			if !gitInert[key] && !strings.HasPrefix(key, "github_app_") {
				return b.failSafe(r, r.name, "source."+key+" may filter versions and has no IR form")
			}
		}
	}
	uri, _ := text(src.Vals["uri"])
	if literalURI(uri) && b.distinctGitURIs() > 1 {
		return b.failSafe(r, r.name, "the resource watches the repository "+uri+", which is not the converted repository: the pipeline has several git uris and only a ((var)) uri names the repository of the pipeline")
	}
	t := ir.Trigger{Kind: ir.TriggerPush, Ext: resourceExt(r.name)}
	t.Paths = ir.Filter{Include: paths, Exclude: ignore}
	if tag != "" {
		if branch != "" {
			return b.failSafe(r, r.name, "tag_filter with branch: Concourse needs the tagged commit on the branch and the IR tag trigger has no branch filter")
		}
		t.Kind = ir.TriggerTag
		t.Tags.Include = []string{tag}
		b.watched(r, caps.TriggerTag, uri, "tags "+tag)
		return sourceTrigger{trig: t}
	}
	if branch == "" {
		return b.failSafe(r, r.name, "source.branch is not set and Concourse follows the default branch")
	}
	t.Branches.Include = []string{branch}
	b.watched(r, caps.TriggerPush, uri, "branch "+branch)
	return sourceTrigger{trig: t, uri: "uri:" + uri, merge: uri != "" && !strings.Contains(uri, varOpen)}
}

// literalURI is true for a git uri that names one fixed repository.
func literalURI(uri string) bool { return uri != "" && !strings.Contains(uri, varOpen) }

// distinctGitURIs counts the different uris of the git resources of the pipeline.
func (b *pipeBuilder) distinctGitURIs() int {
	seen := map[string]bool{}
	for _, r := range b.resOrder {
		if !b.builtin(r, typeGit) {
			continue
		}
		if src := r.source(); src != nil {
			if u, ok := text(src.Vals["uri"]); ok && u != "" {
				seen[u] = true
			}
		}
	}
	return len(seen)
}

// watched says which repository a git trigger watches. The IR trigger belongs to the repository that holds the
// converted pipeline, so the entry names the uri that the Concourse resource follows.
func (b *pipeBuilder) watched(r *resource, f caps.Feature, uri, what string) {
	if uri == "" {
		uri = "(no uri)"
	}
	b.note(f, caps.Emulated, b.path(), "trigger of the converted repository",
		fmt.Sprintf("the git resource %s watches %s (%s); the trigger fires on pushes to the repository that holds the converted pipeline", r.name, uri, what), lineOf(r))
}

var weekdays = map[string]string{"sunday": "0", "monday": "1", "tuesday": "2", "wednesday": "3", "thursday": "4", "friday": "5", "saturday": "6"}

// timeTrigger reads the forms of a time resource that a cron line expresses: a fixed interval that divides the
// hour or the day, and a day list with no window. The cron is approximate, so each use has a report entry.
func (b *pipeBuilder) timeTrigger(r *resource) sourceTrigger {
	if st, ok := b.scheduleMarker(r); ok {
		return st
	}
	src := r.source()
	var interval string
	var days []string
	if src != nil {
		for _, key := range src.Order {
			switch key {
			case "interval":
				interval, _ = text(src.Vals[key])
			case "days":
				l, ok := stringList(src.Vals[key])
				if !ok {
					return b.failSafe(r, r.name, "source.days is not a list")
				}
				for _, d := range l {
					n, known := weekdays[strings.ToLower(d)]
					if !known {
						return b.failSafe(r, r.name, "source.days has the unknown day "+d)
					}
					days = append(days, n)
				}
			case "initial_version":
			case "location":
				if loc, _ := text(src.Vals[key]); loc != "UTC" {
					return b.failSafe(r, r.name, "source.location is not UTC and the IR cron has no time zone")
				}
			default:
				return b.failSafe(r, r.name, "source."+key+" has no cron form")
			}
		}
	}
	cron, ok := timeCron(interval, days)
	if !ok {
		return b.failSafe(r, r.name, "the interval and days have no exact cron line")
	}
	b.note(caps.TriggerSchedule, caps.Emulated, b.path(), "approximate cron",
		fmt.Sprintf("time resource %s becomes the approximate cron %q; Concourse counts the interval from the last version, cron counts from the clock", r.name, cron), r.line)
	ext := resourceExt(r.name)
	ext[dialect].(map[string]any)[ExtCron] = cron
	return sourceTrigger{trig: ir.Trigger{Kind: ir.TriggerSchedule, Cron: cron, Ext: ext}}
}

func timeCron(interval string, days []string) (string, bool) {
	switch {
	case interval == "" && len(days) == 0:
		return "0 0 * * *", true
	case interval == "":
		return "0 0 * * " + strings.Join(days, ","), true
	case len(days) > 0:
		return "", false
	}
	d, err := time.ParseDuration(interval)
	if err != nil || d <= 0 {
		return "", false
	}
	if d%time.Hour == 0 {
		h := int(d / time.Hour)
		switch {
		case h == fullDayHours:
			return "0 0 * * *", true
		case h < fullDayHours && fullDayHours%h == 0:
			return fmt.Sprintf("0 */%d * * *", h), true
		}
		return "", false
	}
	if m := int(d / time.Minute); d%time.Minute == 0 && m < hourMinutes && hourMinutes%m == 0 {
		return fmt.Sprintf("*/%d * * * *", m), true
	}
	return "", false
}

// concurrency maps serial and serial_groups to the pipeline concurrency group when one group covers them all.
func (b *pipeBuilder) concurrency(pl *ir.Pipeline, jobs []*builtJob) {
	groups := map[string]bool{}
	var first string
	for _, j := range jobs {
		for _, g := range j.groups {
			if !groups[g] {
				groups[g] = true
				if first == "" {
					first = g
				}
			}
		}
	}
	switch len(groups) {
	case 0:
	case 1:
		pl.Concurrency = &ir.Concurrency{Group: first}
		b.note(caps.PipelineConcurrency, caps.Emulated, b.path(), "pipeline concurrency group",
			"serial and serial_groups become the pipeline concurrency group "+first+"; Concourse limits only the jobs that name it, and the keys stay in the job Ext", 0)
	default:
		b.note(caps.PipelineConcurrency, caps.Lost, b.path(), strategyExt,
			"serial and serial_groups name several groups, which one pipeline concurrency group cannot hold; the keys stay in the job Ext", 0)
	}
}
