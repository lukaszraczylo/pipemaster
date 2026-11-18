package gha

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

const (
	typeBoolean = "boolean"
	typeNumber  = "number"
	negate      = "!"
)

var plainNumber = regexp.MustCompile(`^(-?[1-9][0-9]*|0|-?(0|[1-9][0-9]*)\.[0-9]*[1-9])$`)

// onBuilder collects events in first-seen order; a second trigger for an event already present is lost.
type onBuilder struct {
	e      *emitter
	events *omap
}

func (b *onBuilder) add(path string, f caps.Feature, name string, n *yaml.Node) {
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

// filterKeys writes key and key-ignore; GHA forbids both on one event, so include and exclude together become "!" entries.
func filterKeys(m *omap, key string, f ir.Filter) {
	switch {
	case len(f.Include) > 0 && len(f.Exclude) > 0:
		all := append([]string{}, f.Include...)
		for _, x := range f.Exclude {
			all = append(all, negate+x)
		}
		m.set(key, strSeq(all))
	case len(f.Include) > 0:
		m.set(key, strSeq(f.Include))
	case len(f.Exclude) > 0:
		m.set(key+"-ignore", strSeq(f.Exclude))
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
	pext := extMap(pl.Ext, e.d.ID)
	used := map[int]bool{}
	var schedule *yaml.Node
	hasManual := false
	for i := range pl.Triggers {
		t := &pl.Triggers[i]
		path := fmt.Sprintf("pipeline/%s/trigger/%d", pl.ID, i)
		var err error
		switch t.Kind {
		case ir.TriggerPush, ir.TriggerTag:
			err = e.pushEvent(b, pl, i, used)
		case ir.TriggerPR:
			err = e.prEvent(b, path, t)
		case ir.TriggerSchedule:
			schedule, err = e.scheduleEntry(schedule, t)
		case ir.TriggerManual:
			hasManual = true
			err = e.dispatchEvent(b, path, pl, t)
		case ir.TriggerAPI:
			err = e.apiEvent(b, path, t)
		case ir.TriggerPipelineDone:
			err = e.workflowRun(b, path, pl, t)
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
	if !hasManual && !b.events.has("workflow_dispatch") {
		if err := e.implicitDispatch(b, pl, jobManual); err != nil {
			return nil, err
		}
	}
	if raw, ok := pext[extEvents].(map[string]any); ok {
		for _, k := range sortedKeys(raw) {
			n, err := plainNode(raw[k])
			if err != nil {
				return nil, err
			}
			b.events.set(k, n)
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

func (e *emitter) pushEvent(b *onBuilder, pl *ir.Pipeline, i int, used map[int]bool) error {
	if used[i] {
		return nil
	}
	used[i] = true
	t := &pl.Triggers[i]
	path := fmt.Sprintf("pipeline/%s/trigger/%d", pl.ID, i)
	var push, tag *ir.Trigger
	if t.Kind == ir.TriggerPush {
		push = t
		for j := i + 1; j < len(pl.Triggers); j++ {
			if pl.Triggers[j].Kind == ir.TriggerTag && !used[j] {
				used[j], tag = true, &pl.Triggers[j]
				break
			}
		}
	} else {
		tag = t
	}
	m := newMap()
	paths := ir.Filter{}
	if push != nil {
		filterKeys(m, "branches", push.Branches)
		paths = push.Paths
	}
	if tag != nil {
		filterKeys(m, "tags", tag.Tags)
		if push == nil {
			paths = tag.Paths
		}
	}
	filterKeys(m, "paths", paths)
	for _, t := range []*ir.Trigger{push, tag} {
		if t == nil {
			continue
		}
		if err := m.addExtra(extMap(t.Ext, e.d.ID), nil); err != nil {
			return err
		}
	}
	b.add(path, caps.TriggerPush, "push", orNull(m))
	return nil
}

func (e *emitter) prEvent(b *onBuilder, path string, t *ir.Trigger) error {
	ext := extMap(t.Ext, e.d.ID)
	name := evPR
	if ext[extEvent] == evPRTarget {
		name = evPRTarget
	}
	m := newMap()
	filterKeys(m, "branches", t.Branches)
	filterKeys(m, "paths", t.Paths)
	if err := m.addExtra(ext, map[string]bool{extEvent: true}); err != nil {
		return err
	}
	b.add(path, caps.TriggerPullRequest, name, orNull(m))
	return nil
}

func (e *emitter) scheduleEntry(seq *yaml.Node, t *ir.Trigger) (*yaml.Node, error) {
	if seq == nil {
		seq = seqOf()
	}
	m := newMap()
	m.set("cron", str(t.Cron))
	if err := m.addExtra(extMap(t.Ext, e.d.ID), nil); err != nil {
		return nil, err
	}
	seq.Content = append(seq.Content, m.n)
	return seq, nil
}

func (e *emitter) dispatchEvent(b *onBuilder, path string, pl *ir.Pipeline, t *ir.Trigger) error {
	m, err := e.dispatchBody(path, pl, extMap(t.Ext, e.d.ID))
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
	ext := extMap(t.Ext, e.d.ID)
	name, _ := ext[extEvent].(string)
	if name == "" {
		name = evRepoDisp
	}
	m, err := eventMap(ext, extEvent)
	if err != nil {
		return err
	}
	b.add(path, caps.TriggerAPI, name, orNull(m))
	return nil
}

// workflowRun needs a workflows list; without one it names its own workflow, the only name known here.
func (e *emitter) workflowRun(b *onBuilder, path string, pl *ir.Pipeline, t *ir.Trigger) error {
	ext := extMap(t.Ext, e.d.ID)
	m := newMap()
	if raw, ok := ext["workflows"]; ok {
		n, err := plainNode(raw)
		if err != nil {
			return err
		}
		m.set("workflows", n)
	} else {
		m.set("workflows", strSeq([]string{e.workflowName(pl)}))
	}
	filterKeys(m, "branches", t.Branches)
	if err := m.addExtra(ext, nil); err != nil {
		return err
	}
	b.add(path, caps.TriggerPipelineDone, evWorkflowRn, m.n)
	return nil
}

func (e *emitter) workflowName(pl *ir.Pipeline) string {
	if pl.Name != "" {
		return pl.Name
	}
	return e.d.Dir + "/" + pl.ID + ".yml"
}
