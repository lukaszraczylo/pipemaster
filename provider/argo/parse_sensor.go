package argo

import (
	"regexp"
	"slices"
	"strings"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	pathEvent      = "headers.X-Github-Event"
	pathRef        = "body.ref"
	pathBaseRef    = "body.pull_request.base.ref"
	pathAction     = "body.action"
	pathPRState    = "body.pull_request.state"
	eventPush      = "push"
	eventPR        = "pull_request"
	refHeads       = "refs/heads/"
	refTags        = "refs/tags/"
	logicalAnd     = "and"
	typeString     = "string"
	comparatorEq   = "="
	prStateOpen    = "open"
	globAnyTail    = ".*"
	globStar       = "*"
	regexAnchorBeg = "^"
	regexAnchorEnd = "$"
)

// defaultPRActions are the pull request actions that the IR pull request trigger starts on.
var defaultPRActions = []string{"opened", "reopened", "synchronize"}

// refName is the characters of a branch or tag name that a regex matches as itself.
var refChars = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// usedEvent names the EventSource entry that a trigger consumed.
type usedEvent struct{ source, entry string }

// dataFilter is one data filter of a dependency.
type dataFilter struct {
	path   string
	values []string
}

// dependency maps one dependency to triggers. why is empty when it succeeded; otherwise it says what has no IR form.
func (p *project) dependency(dep *omap, k *sink) (out []ir.Trigger, used usedEvent, why string) {
	srcName, _ := text(dep.Vals["eventSourceName"])
	eventName, _ := text(dep.Vals["eventName"])
	es := p.sources[srcName]
	if es == nil {
		return nil, used, "EventSource " + srcName + " is not in the repo"
	}
	espec, _ := asMap(es.Top.Vals["spec"])
	typ, entry := eventEntry(espec, eventName)
	if entry == nil {
		return nil, used, "EventSource " + srcName + " has no event " + eventName
	}
	if typ != typeGithub {
		return nil, used, "event source type " + typ + " has no IR trigger"
	}
	for _, key := range dep.Order {
		if !slices.Contains([]string{"name", "eventSourceName", "eventName", "filters"}, key) {
			return nil, used, "the dependency key " + key + " has no IR form"
		}
	}
	filters, why := dataFilters(dep)
	if why != "" {
		return nil, used, why
	}
	kinds, why := p.githubKinds(entry, filters)
	if why != "" {
		return nil, used, why
	}
	trs, why := p.buildTriggers(kinds, filters, k)
	if why != "" {
		return nil, used, why
	}
	extra := map[string]any{"dependency": plain(dep), "eventSource": p.eventSourceExt(es, espec, typ, eventName, entry, k)}
	for i := range trs {
		trs[i].Ext = ir.Ext{dialect: extra}
	}
	return trs, usedEvent{srcName, typ + "/" + eventName}, ""
}

// eventEntry finds the event entry that eventName names, with its type.
func eventEntry(spec *omap, eventName string) (string, *omap) {
	if spec == nil {
		return "", nil
	}
	for _, typ := range spec.Order {
		if !eventTypes[typ] {
			continue
		}
		if events, ok := asMap(spec.Vals[typ]); ok {
			if e, ok := asMap(events.Vals[eventName]); ok {
				return typ, e
			}
		}
	}
	return "", nil
}

// eventSourceExt is the part of an EventSource that a trigger keeps: its name, metadata, the event entry and the
// spec keys that configure the pod.
func (p *project) eventSourceExt(es *rawDoc, spec *omap, typ, event string, entry *omap, k *sink) map[string]any {
	m := map[string]any{"name": es.Name(), "type": typ, "event": event, "entry": plain(entry)}
	meta, _ := asMap(es.Top.Vals["metadata"])
	if rest := metaRest(meta); len(rest.Order) > 0 {
		m["metadata"] = plain(rest)
	}
	var cfg []string
	for _, key := range spec.Order {
		if !eventTypes[key] {
			cfg = append(cfg, key)
		}
	}
	if len(cfg) > 0 {
		m["spec"] = plain(spec.Subset(cfg))
	}
	k.Note(caps.ExtPassthrough, "EventSource "+es.Name()+": the event entry "+event+" (repositories, webhook, secrets), metadata and pod keys have no IR field; kept in Ext", es.Line)
	return m
}

// dataFilters reads the filters of a dependency. Only data filters that combine with and have a reading.
func dataFilters(dep *omap) ([]dataFilter, string) {
	fm, ok := asMap(dep.Vals["filters"])
	if !ok {
		return nil, ""
	}
	var out []dataFilter
	for _, key := range fm.Order {
		switch key {
		case "data":
		case "dataLogicalOperator":
			if op, _ := text(fm.Vals[key]); op != "" && op != logicalAnd {
				return nil, "the data filters combine with " + op
			}
		default:
			return nil, "the filter " + key + " has no IR form"
		}
	}
	seen := map[string]bool{}
	for _, e := range listOf(fm.Vals["data"]) {
		m, _ := asMap(e)
		path, _ := text(valueOf(m, "path"))
		typ, _ := text(valueOf(m, "type"))
		cmp, _ := text(valueOf(m, "comparator"))
		vals, isList := stringList(valueOf(m, "value"))
		switch {
		case m == nil || path == "" || !isList || typ != typeString || (cmp != "" && cmp != comparatorEq):
			return nil, "a data filter on " + path + " is not a string match"
		case slices.ContainsFunc(m.Order, func(k string) bool { return !slices.Contains([]string{"path", "type", "value", "comparator"}, k) }):
			return nil, "a data filter on " + path + " has a template"
		case seen[path]:
			return nil, "the path " + path + " has two data filters"
		}
		seen[path] = true
		out = append(out, dataFilter{path: path, values: vals})
	}
	return out, ""
}

// githubKinds reads the event kinds from the event header filter, or from the events of the EventSource.
func (p *project) githubKinds(entry *omap, filters []dataFilter) (kinds []string, why string) {
	events, _ := textList(entry.Vals["events"])
	for _, f := range filters {
		if f.path != pathEvent {
			continue
		}
		for _, v := range f.values {
			if v != eventPush && v != eventPR {
				return nil, "the event " + v + " has no IR trigger"
			}
			if !slices.Contains(events, v) && !slices.Contains(events, globStar) {
				return nil, "the event " + v + " is not served by the EventSource"
			}
			kinds = append(kinds, v)
		}
		return kinds, ""
	}
	for _, e := range events {
		if e != eventPush && e != eventPR {
			return nil, "the EventSource serves " + e + " events and the Sensor does not filter them"
		}
		kinds = append(kinds, e)
	}
	if len(kinds) == 0 {
		return nil, "no event kind is known"
	}
	return kinds, ""
}

// buildTriggers makes one trigger per event kind with the branch and tag filters that the data filters hold.
func (p *project) buildTriggers(kinds []string, filters []dataFilter, k *sink) ([]ir.Trigger, string) {
	var branches, tags, base []string
	for _, f := range filters {
		var err string
		switch f.path {
		case pathEvent:
		case pathRef:
			branches, tags, err = p.refGlobs(f.values, k)
		case pathBaseRef:
			base, err = p.baseGlobs(f.values, k)
		case pathAction:
			if !sameSet(f.values, defaultPRActions) {
				err = "the pull request actions are not opened, synchronize and reopened"
			}
		case pathPRState:
			if !sameSet(f.values, []string{prStateOpen}) {
				err = "the pull request state is not open"
			}
		default:
			err = "a data filter on " + f.path + " has no IR form"
		}
		if err != "" {
			return nil, err
		}
	}
	var out []ir.Trigger
	for _, kind := range kinds {
		tr := ir.Trigger{Kind: ir.TriggerPush}
		switch {
		case kind == eventPR && len(branches)+len(tags) > 0:
			return nil, "a body.ref filter does not apply to pull request events"
		case kind == eventPR:
			tr.Kind, tr.Branches = ir.TriggerPR, ir.Filter{Include: base}
		case len(base) > 0:
			return nil, "a pull request base filter does not apply to push events"
		case len(branches) > 0 && len(tags) > 0:
			return nil, "the ref filter holds branches and tags"
		case len(tags) > 0:
			tr.Kind, tr.Tags = ir.TriggerTag, ir.Filter{Include: tags}
		default:
			tr.Branches = ir.Filter{Include: branches}
		}
		out = append(out, tr)
	}
	return out, ""
}

func sameSet(got, want []string) bool {
	g := slices.Clone(got)
	slices.Sort(g)
	w := slices.Clone(want)
	slices.Sort(w)
	return slices.Equal(g, w)
}

// refGlobs splits body.ref values into branch and tag globs.
func (p *project) refGlobs(values []string, k *sink) (branches, tags []string, why string) {
	for _, v := range values {
		body, anchored := stripAnchors(v)
		var dst *[]string
		switch {
		case strings.HasPrefix(body, refHeads):
			body, dst = strings.TrimPrefix(body, refHeads), &branches
		case strings.HasPrefix(body, refTags):
			body, dst = strings.TrimPrefix(body, refTags), &tags
		default:
			return nil, nil, "the ref pattern " + v + " names neither a branch nor a tag"
		}
		g, ok := regexGlob(body)
		if !ok {
			return nil, nil, "the ref pattern " + v + " is a regex with no glob form"
		}
		*dst = append(*dst, g)
		p.unanchored(k, v, anchored)
	}
	return branches, tags, ""
}

// baseGlobs reads body.pull_request.base.ref values as branch globs.
func (p *project) baseGlobs(values []string, k *sink) ([]string, string) {
	var out []string
	for _, v := range values {
		body, anchored := stripAnchors(v)
		g, ok := regexGlob(body)
		if !ok {
			return nil, "the base ref pattern " + v + " is a regex with no glob form"
		}
		out = append(out, g)
		p.unanchored(k, v, anchored)
	}
	return out, ""
}

// unanchored reports a pattern that Argo matches anywhere in the ref; the IR holds the exact name.
func (p *project) unanchored(k *sink, pattern string, anchored bool) {
	if anchored {
		return
	}
	p.rep.Add(report.Entry{
		IRPath: k.Path, Feature: caps.TriggerFilterBranch, Level: caps.Emulated, Strategy: "exact name", SourceLoc: k.File,
		Detail: "the pattern " + pattern + " has no anchors, so Argo matches it anywhere in the ref; the IR filter matches the whole name",
	})
}

// stripAnchors removes ^ and $ and reports whether both were there.
func stripAnchors(v string) (string, bool) {
	body, hasStart := strings.CutPrefix(v, regexAnchorBeg)
	body, hasEnd := strings.CutSuffix(body, regexAnchorEnd)
	return body, hasStart && hasEnd
}

// regexGlob reads a regex that is a plain name, or a name followed by .*, as a glob.
func regexGlob(body string) (string, bool) {
	prefix, star := strings.CutSuffix(body, globAnyTail)
	if prefix != "" && !refChars.MatchString(prefix) || prefix == "" && !star {
		return "", false
	}
	if star {
		return prefix + globStar, true
	}
	return prefix, true
}
