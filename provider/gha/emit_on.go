package gha

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	typeBoolean = "boolean"
	typeNumber  = "number"
	typeString  = "string"
	negate      = "!"
)

// dispatchTypes lists the input types that GHA accepts.
var dispatchTypes = map[string]bool{typeString: true, typeNumber: true, typeBoolean: true, "choice": true, "environment": true}

var plainNumber = regexp.MustCompile(`^(-?[1-9][0-9]*|0|-?(0|[1-9][0-9]*)\.[0-9]*[1-9])$`)

// onBuilder collects events in first-seen order; a second trigger for an event already present is lost.
type onBuilder struct {
	e      *emitter
	events *omap
}

func (b *onBuilder) add(path string, f caps.Feature, name string, n *yaml.Node) {
	if !b.e.d.accepts(name) {
		b.e.lose(path, f, "event "+name+" is not supported by "+b.e.d.title()+"; the trigger is dropped")
		return
	}
	if b.events.has(name) {
		b.e.lose(path, f, "event "+name+" already has a trigger; GHA allows one filter set per event")
		return
	}
	b.events.set(name, n)
}

func eventMap(ext map[string]any, skip ...string) (*omap, error) {
	m := newMap()
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}
	return m, m.addExtra(ext, skipSet)
}

func hasFilter(f ir.Filter) bool { return len(f.Include) > 0 || len(f.Exclude) > 0 }

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameFilter(a, b ir.Filter) bool {
	return sameStrings(a.Include, b.Include) && sameStrings(a.Exclude, b.Exclude)
}

// filterKeys writes key and key-ignore; GHA forbids both on one event, so include and exclude together
// become "!" entries and the ledger says so.
func (e *emitter) filterKeys(m *omap, key string, f ir.Filter, path string, feature caps.Feature) error {
	switch {
	case len(f.Include) > 0 && len(f.Exclude) > 0:
		all := append([]string{}, f.Include...)
		for _, x := range f.Exclude {
			all = append(all, negate+x)
		}
		m.set(key, strSeq(all))
		e.emulate(path, feature, "exclusions written as ! entries", "GHA cannot combine "+key+" and "+key+"-ignore on one event")
		return e.markArg(feature, path, f)
	case len(f.Include) > 0:
		m.set(key, strSeq(f.Include))
	case len(f.Exclude) > 0:
		m.set(key+"-ignore", strSeq(f.Exclude))
	}
	return nil
}

// loseFilters reports every filter of t that its GHA event has no key for.
func (e *emitter) loseFilters(path string, t *ir.Trigger, branches, tags, paths bool) {
	for _, f := range []struct {
		feature caps.Feature
		flt     ir.Filter
		allowed bool
	}{
		{caps.TriggerFilterBranch, t.Branches, branches},
		{caps.TriggerFilterTags, t.Tags, tags},
		{caps.TriggerFilterPaths, t.Paths, paths},
	} {
		if !f.allowed && hasFilter(f.flt) {
			e.lose(path, f.feature, "this trigger kind has no GHA key for the filter")
		}
	}
}

func orNull(m *omap) *yaml.Node {
	if m.empty() {
		return nullNode()
	}
	return m.n
}

// on builds the on: mapping; jobManual names a job.manual path that needs a workflow_dispatch trigger.
func (e *emitter) on(pl *ir.Pipeline, jobManual string) (*yaml.Node, error) {
	b := &onBuilder{e: e, events: newMap()}
	pext := e.ext(pl.Ext)
	used := map[int]bool{}
	var schedule *yaml.Node
	hasManual := false
	for i := range pl.Triggers {
		t := &pl.Triggers[i]
		path := fmt.Sprintf("pipeline/%s/trigger/%d", pl.ID, i)
		var err error
		e.foreignExt(path, t.Ext)
		switch t.Kind {
		case ir.TriggerPush, ir.TriggerTag:
			err = e.pushEvent(b, pl, i, used)
		case ir.TriggerPR:
			e.loseFilters(path, t, true, false, true)
			err = e.prEvent(b, path, t)
		case ir.TriggerSchedule:
			e.loseFilters(path, t, false, false, false)
			schedule, err = e.scheduleEntry(schedule, path, t)
		case ir.TriggerManual:
			hasManual = true
			e.loseFilters(path, t, false, false, false)
			err = e.dispatchEvent(b, path, pl, t)
		case ir.TriggerAPI:
			e.loseFilters(path, t, false, false, false)
			err = e.apiEvent(b, path, t)
		case ir.TriggerPipelineDone:
			e.loseFilters(path, t, true, false, false)
			err = e.workflowRun(b, path, t)
		case ir.TriggerResource:
		default:
			err = fmt.Errorf("%s: trigger has unsupported kind %q", path, t.Kind)
		}
		if err != nil {
			return nil, err
		}
	}
	if schedule != nil {
		b.events.set("schedule", schedule)
	}
	if raw, ok := pext[extEvents].(map[string]any); ok {
		for _, k := range sortedKeys(raw) {
			if !e.d.accepts(k) {
				e.lose("pipeline/"+pl.ID+eventSeg+k, caps.ExtPassthrough, "event "+k+" is not supported by "+e.d.title()+"; dropped")
				continue
			}
			n, err := plainNode(raw[k])
			if err != nil {
				return nil, err
			}
			b.events.set(k, n)
		}
	}
	if !hasManual && !b.events.has("workflow_dispatch") {
		if err := e.implicitDispatch(b, pl, jobManual); err != nil {
			return nil, err
		}
	}
	return b.events.n, nil
}

// implicitDispatch adds workflow_dispatch when inputs, a manual job or an empty trigger list needs it.
func (e *emitter) implicitDispatch(b *onBuilder, pl *ir.Pipeline, jobManual string) error {
	pp := "pipeline/" + pl.ID
	var marker string
	switch {
	case jobManual != "":
		marker = emitkit.Marker(caps.JobManual, jobManual)
		e.mark(caps.TriggerManual, pp)
	case len(pl.Inputs) > 0:
		marker = emitkit.Marker(caps.PipelineInputs, pp)
		e.emulate(pp, caps.PipelineInputs, "workflow_dispatch trigger", "inputs need a workflow_dispatch trigger; the pipeline had none")
	case b.events.empty():
		marker = emitkit.Marker(caps.TriggerManual, pp)
	default:
		return nil
	}
	m, err := e.dispatchBody(pp, pl, nil)
	if err != nil {
		return err
	}
	b.events.setComment("workflow_dispatch", orNull(m), []string{marker})
	return nil
}

const matchAll = "**"

// pushEvent writes the push trigger and the tag trigger it pairs with as one push: event. A push event with a
// filter for one ref kind fires only for the kinds a filter names, so an unfiltered side is written as '**'.
func (e *emitter) pushEvent(b *onBuilder, pl *ir.Pipeline, i int, used map[int]bool) error {
	if used[i] {
		return nil
	}
	used[i] = true
	t := &pl.Triggers[i]
	var push, tag *ir.Trigger
	pp, tp := fmt.Sprintf("pipeline/%s/trigger/%d", pl.ID, i), ""
	if t.Kind == ir.TriggerPush {
		push = t
		for j := i + 1; j < len(pl.Triggers); j++ {
			if pl.Triggers[j].Kind == ir.TriggerTag && !used[j] {
				used[j], tag = true, &pl.Triggers[j]
				tp = fmt.Sprintf("pipeline/%s/trigger/%d", pl.ID, j)
				break
			}
		}
	} else {
		tag, tp, pp = t, pp, ""
	}
	m := newMap()
	paths := ir.Filter{}
	// An unfiltered push and tag pair is a bare push event, which GHA fires for both ref kinds.
	bare := push != nil && tag != nil && !hasFilter(push.Branches) && !hasFilter(tag.Tags)
	if push != nil {
		if hasFilter(push.Tags) {
			e.lose(pp, caps.TriggerFilterTags, "a push trigger has no tag filter; tags belong to a tag trigger")
		}
		if !hasFilter(push.Branches) && !bare {
			e.emulate(pp, caps.TriggerPush, "branches: '**'", "GHA fires a push without filters for branches and tags; branches are named explicitly to leave tags out or beside a tag filter")
			m.set("branches", strSeq([]string{matchAll}))
			e.mark(caps.TriggerPush, pp)
		}
		if err := e.filterKeys(m, "branches", push.Branches, pp, caps.TriggerFilterBranch); err != nil {
			return err
		}
		paths = push.Paths
	}
	if tag != nil {
		if hasFilter(tag.Branches) {
			e.lose(tp, caps.TriggerFilterBranch, "a tag trigger has no branch filter")
		}
		if push != nil && !sameFilter(push.Paths, tag.Paths) {
			e.lose(tp, caps.TriggerFilterPaths, "the push trigger's paths filter applies to tags too; GHA has one per push event")
		}
		if !hasFilter(tag.Tags) && !bare {
			e.emulate(tp, caps.TriggerTag, "tags: '**'", "GHA fires tag pushes only when a tags filter names them")
			m.set("tags", strSeq([]string{matchAll}))
			e.mark(caps.TriggerTag, tp)
		}
		if err := e.filterKeys(m, "tags", tag.Tags, tp, caps.TriggerFilterTags); err != nil {
			return err
		}
		if push == nil {
			paths = tag.Paths
		}
	}
	if err := e.filterKeys(m, "paths", paths, firstNonEmpty(pp, tp), caps.TriggerFilterPaths); err != nil {
		return err
	}
	for _, t := range []*ir.Trigger{push, tag} {
		if t == nil {
			continue
		}
		if err := m.addExtra(e.ext(t.Ext), nil); err != nil {
			return err
		}
	}
	if push != nil && tag != nil && len(e.ext(tag.Ext)) > 0 {
		e.emulate(tp, caps.ExtPassthrough, "merged into the push event", "the tag trigger's native keys sit on the shared push event")
	}
	b.add(firstNonEmpty(pp, tp), caps.TriggerPush, "push", orNull(m))
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (e *emitter) prEvent(b *onBuilder, path string, t *ir.Trigger) error {
	ext := e.ext(t.Ext)
	name := evPR
	if ext[extEvent] == evPRTarget {
		name = evPRTarget
	}
	m := newMap()
	if err := e.filterKeys(m, "branches", t.Branches, path, caps.TriggerFilterBranch); err != nil {
		return err
	}
	if err := e.filterKeys(m, "paths", t.Paths, path, caps.TriggerFilterPaths); err != nil {
		return err
	}
	if err := m.addExtra(ext, map[string]bool{extEvent: true}); err != nil {
		return err
	}
	if e.origin == provider.GitLab && name == evPR {
		e.emulate(path, caps.TriggerPullRequest, "pull_request: types: [opened, synchronize]", "GitLab runs a merge request pipeline in the fork project, but a GHA pull_request run from a fork is a run of the base repository; the jobs that admit the pull request also run for fork pull requests")
	}
	b.add(path, caps.TriggerPullRequest, name, orNull(m))
	return nil
}

func (e *emitter) scheduleEntry(seq *yaml.Node, path string, t *ir.Trigger) (*yaml.Node, error) {
	cron := t.Cron
	if cron == "" {
		if e.origin == provider.GitLab {
			e.externalSchedule(path)
		} else {
			e.lose(path, caps.TriggerSchedule, "the schedule names no cron expression; GHA needs one")
		}
		return seq, nil
	}
	if seq == nil {
		seq = seqOf()
	}
	m := newMap()
	m.set("cron", str(cron))
	if err := m.addExtra(e.ext(t.Ext), nil); err != nil {
		return nil, err
	}
	seq.Content = append(seq.Content, m.n)
	return seq, nil
}

func (e *emitter) dispatchEvent(b *onBuilder, path string, pl *ir.Pipeline, t *ir.Trigger) error {
	m, err := e.dispatchBody(path, pl, e.ext(t.Ext))
	if err != nil {
		return err
	}
	b.add(path, caps.TriggerManual, "workflow_dispatch", orNull(m))
	return nil
}

func (e *emitter) dispatchBody(path string, pl *ir.Pipeline, ext map[string]any) (*omap, error) {
	m := newMap()
	extras, _ := ext[extInputs].(map[string]any)
	if len(pl.Inputs) > 0 {
		in := newMap()
		for _, input := range pl.Inputs {
			if e.from != nil && input.Type != "" && !dispatchTypes[input.Type] {
				e.lose(path, caps.PipelineInputs, "input "+input.Name+" has type "+input.Type+", which GHA does not know; it is written as a string")
				input.Type = typeString
			}
			n, err := inputNode(input, extras[input.Name])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			in.set(input.Name, n)
		}
		m.set(extInputs, in.n)
	}
	return m, m.addExtra(ext, map[string]bool{extInputs: true})
}

func inputNode(in ir.Input, extra any) (*yaml.Node, error) {
	m := newMap()
	if in.Description != "" {
		m.set("description", str(in.Description))
	}
	if in.Required {
		m.set("required", boolNodeOf(true))
	}
	if in.Default != "" {
		m.set("default", typedScalar(in.Default, in.Type))
	}
	if in.Type != "" {
		m.set("type", str(in.Type))
	}
	if x, ok := extra.(map[string]any); ok {
		if err := m.addExtra(x, nil); err != nil {
			return nil, err
		}
	}
	return orNull(m), nil
}

// typedScalar writes a default as a YAML boolean or number only when the text survives the parser's resolution unchanged.
func typedScalar(s, typ string) *yaml.Node {
	switch {
	case typ == typeBoolean && (s == "true" || s == "false"):
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tagBool, Value: s}
	case typ == typeNumber && plainNumber.MatchString(s):
		tag := tagInt
		if strings.Contains(s, ".") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: s}
	}
	return str(s)
}

func (e *emitter) apiEvent(b *onBuilder, path string, t *ir.Trigger) error {
	ext := e.ext(t.Ext)
	name, _ := ext[extEvent].(string)
	if name == "" {
		name = evRepoDisp
		e.emulate(path, caps.TriggerAPI, evRepoDisp, "the API trigger names no event; repository_dispatch is used")
		e.mark(caps.TriggerAPI, path)
	}
	m, err := eventMap(ext, extEvent)
	if err != nil {
		return err
	}
	b.add(path, caps.TriggerAPI, name, orNull(m))
	return nil
}

// workflowRun needs the upstream workflow names; the IR holds none for a pipeline from another provider.
func (e *emitter) workflowRun(b *onBuilder, path string, t *ir.Trigger) error {
	ext := e.ext(t.Ext)
	raw, ok := ext["workflows"]
	if !ok {
		e.lose(path, caps.TriggerPipelineDone, "no upstream workflow named")
		return nil
	}
	n, err := plainNode(raw)
	if err != nil {
		return err
	}
	m := newMap()
	m.set("workflows", n)
	if err := e.filterKeys(m, "branches", t.Branches, path, caps.TriggerFilterBranch); err != nil {
		return err
	}
	if err := m.addExtra(ext, nil); err != nil {
		return err
	}
	b.add(path, caps.TriggerPipelineDone, evWorkflowRn, m.n)
	return nil
}

// noteDefaultEvents reports the events a source runs on when its file names no trigger and the workflow has no event
// for: each is lost.
func (e *emitter) noteDefaultEvents(noForm map[string][]provider.SourceEvent) {
	for id, events := range noForm {
		for _, g := range provider.GroupByFeature(events) {
			e.lose("pipeline/"+id, g.Feature, "the source runs on the events "+strings.Join(g.Names, ", ")+" and its file has no trigger form for them; "+e.d.title()+" has no event for them")
		}
	}
}
