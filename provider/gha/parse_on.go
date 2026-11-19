package gha

import (
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// Ext keys the trigger parser writes under the dialect's Ext entry.
const (
	extEvent     = "event"
	extEvents    = "events"
	extInputs    = "inputs"
	evPR         = "pull_request"
	evPRTarget   = "pull_request_target"
	evPush       = "push"
	evSchedule   = "schedule"
	evDispatch   = "workflow_dispatch"
	evCall       = "workflow_call"
	evRepoDisp   = "repository_dispatch"
	evWorkflowRn = "workflow_run"
)

type event struct {
	val  *yaml.Node
	name string
}

// onResult is everything the on: key contributes to the pipeline.
type onResult struct {
	other    map[string]any
	triggers []ir.Trigger
	inputs   []ir.Input
}

// events flattens the three on: forms (string, list, map) into one ordered event list.
func (fp *fileParser) events(n *yaml.Node) ([]event, error) {
	n = deref(n)
	switch n.Kind {
	case yaml.ScalarNode:
		if isNull(n) {
			return nil, nil
		}
		s, _ := scalarText(n)
		return []event{{name: s}}, nil
	case yaml.SequenceNode:
		var out []event
		seen := map[string]bool{}
		for _, c := range n.Content {
			s, ok := scalarText(c)
			if !ok {
				return nil, fp.errf(deref(c), "on entries must be event names")
			}
			if !seen[s] {
				seen[s] = true
				out = append(out, event{name: s})
			}
		}
		return out, nil
	case yaml.MappingNode:
		m, err := fp.mapping(n, "on")
		if err != nil {
			return nil, err
		}
		out := make([]event, 0, len(m.entries))
		for _, e := range m.entries {
			out = append(out, event{name: e.key, val: e.val})
		}
		return out, nil
	}
	return nil, fp.errf(n, "on must be a string, a list or a mapping")
}

func (fp *fileParser) parseOn(n *yaml.Node) (*onResult, error) {
	evs, err := fp.events(n)
	if err != nil {
		return nil, err
	}
	res := &onResult{}
	for _, ev := range evs {
		if err := fp.parseEvent(res, ev); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func isNull(n *yaml.Node) bool {
	n = deref(n)
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

func (fp *fileParser) parseEvent(res *onResult, ev event) error {
	if ev.name == evSchedule {
		return fp.parseSchedule(res, ev)
	}
	var m *mapping
	if !isNull(ev.val) {
		var err error
		if m, err = fp.mapping(ev.val, "on."+ev.name); err != nil {
			return err
		}
	} else {
		m = &mapping{fp: fp, used: map[string]bool{}}
	}
	switch ev.name {
	case evPush:
		return fp.parsePush(res, m)
	case evPR, evPRTarget:
		return fp.parsePR(res, m, ev.name)
	case evDispatch:
		return fp.parseDispatch(res, m)
	case evCall, evRepoDisp:
		return fp.parseAPI(res, m, ev.name)
	case evWorkflowRn:
		return fp.parseWorkflowRun(res, m)
	}
	var raw any
	if ev.val != nil && !isNull(ev.val) {
		var err error
		if raw, err = fp.plain(ev.val); err != nil {
			return err
		}
	}
	if res.other == nil {
		res.other = map[string]any{}
	}
	res.other[ev.name] = raw
	return nil
}

// filter reads an include key and its -ignore twin into one Filter. Pattern order is kept because a
// later "!" entry overrides an earlier one.
func (fp *fileParser) filter(m *mapping, key string) (f ir.Filter, present bool) {
	if n := m.take(key); n != nil {
		if l, ok := stringList(n); ok {
			f.Include, present = l, true
		} else {
			m.release(key)
		}
	}
	if n := m.take(key + "-ignore"); n != nil {
		if l, ok := stringList(n); ok {
			f.Exclude, present = l, true
		} else {
			m.release(key + "-ignore")
		}
	}
	return f, present
}

func finishTrigger(t *ir.Trigger, m *mapping) error {
	ext, err := m.ext()
	t.Ext = ext
	return err
}

func (fp *fileParser) parsePush(res *onResult, m *mapping) error {
	branches, hasB := fp.filter(m, "branches")
	tags, hasT := fp.filter(m, "tags")
	paths, _ := fp.filter(m, "paths")
	first := len(res.triggers)
	// GHA fires a push with neither filter for branches and tags.
	bare := !hasB && !hasT
	if hasB || bare {
		res.triggers = append(res.triggers, ir.Trigger{Kind: ir.TriggerPush, Branches: branches, Paths: paths})
	}
	if hasT || bare {
		res.triggers = append(res.triggers, ir.Trigger{Kind: ir.TriggerTag, Tags: tags, Paths: paths})
	}
	return finishTrigger(&res.triggers[first], m)
}

func (fp *fileParser) parsePR(res *onResult, m *mapping, name string) error {
	branches, _ := fp.filter(m, "branches")
	paths, _ := fp.filter(m, "paths")
	t := ir.Trigger{Kind: ir.TriggerPR, Branches: branches, Paths: paths}
	if name == evPRTarget {
		m.keep(extEvent, evPRTarget)
	}
	if err := finishTrigger(&t, m); err != nil {
		return err
	}
	res.triggers = append(res.triggers, t)
	return nil
}

func (fp *fileParser) parseSchedule(res *onResult, ev event) error {
	n := deref(ev.val)
	if n == nil || n.Kind != yaml.SequenceNode {
		return fp.errf(n, "on.schedule must be a list of cron entries")
	}
	for _, c := range n.Content {
		m, err := fp.mapping(c, "on.schedule entry")
		if err != nil {
			return err
		}
		cn := m.take("cron")
		if cn == nil {
			return fp.errf(deref(c), "on.schedule entry needs cron")
		}
		cron, err := fp.scalar(cn, "cron")
		if err != nil {
			return err
		}
		t := ir.Trigger{Kind: ir.TriggerSchedule, Cron: cron}
		if err := finishTrigger(&t, m); err != nil {
			return err
		}
		res.triggers = append(res.triggers, t)
	}
	return nil
}

// parseDispatch maps workflow_dispatch to Manual. Its inputs become pipeline inputs; keys of an
// input the IR has no field for (options, …) stay in the trigger's Ext under inputs.<name>.
func (fp *fileParser) parseDispatch(res *onResult, m *mapping) error {
	t := ir.Trigger{Kind: ir.TriggerManual}
	if n := m.take(extInputs); n != nil {
		extras, err := fp.dispatchInputs(res, n)
		if err != nil {
			return err
		}
		if extras != nil {
			m.keep(extInputs, extras)
		}
	}
	if err := finishTrigger(&t, m); err != nil {
		return err
	}
	res.triggers = append(res.triggers, t)
	return nil
}

func (fp *fileParser) dispatchInputs(res *onResult, n *yaml.Node) (map[string]any, error) {
	im, err := fp.mapping(n, "workflow_dispatch inputs")
	if err != nil {
		return nil, err
	}
	var extras map[string]any
	for _, e := range im.entries {
		in := ir.Input{Name: e.key}
		var rest map[string]any
		if !isNull(e.val) {
			om, err := fp.mapping(e.val, "input "+e.key)
			if err != nil {
				return nil, err
			}
			fillInput(&in, om)
			if rest, err = om.rest(); err != nil {
				return nil, err
			}
		}
		res.inputs = append(res.inputs, in)
		if rest != nil {
			if extras == nil {
				extras = map[string]any{}
			}
			extras[e.key] = rest
		}
	}
	return extras, nil
}

func fillInput(in *ir.Input, om *mapping) {
	for _, f := range []struct {
		dst *string
		key string
	}{{&in.Description, "description"}, {&in.Type, "type"}, {&in.Default, "default"}} {
		n := om.take(f.key)
		if n == nil {
			continue
		}
		s, ok := scalarText(n)
		if !ok {
			om.release(f.key)
			continue
		}
		*f.dst = s
	}
	if n := om.take("required"); n != nil {
		b, ok := boolNode(n)
		if !ok {
			om.release("required")
		}
		in.Required = b
	}
}

// parseAPI keeps the whole event config in Ext: no IR field models workflow_call or repository_dispatch.
func (fp *fileParser) parseAPI(res *onResult, m *mapping, name string) error {
	t := ir.Trigger{Kind: ir.TriggerAPI}
	m.keep(extEvent, name)
	if err := finishTrigger(&t, m); err != nil {
		return err
	}
	res.triggers = append(res.triggers, t)
	return nil
}

func (fp *fileParser) parseWorkflowRun(res *onResult, m *mapping) error {
	branches, _ := fp.filter(m, "branches")
	t := ir.Trigger{Kind: ir.TriggerPipelineDone, Branches: branches}
	if err := finishTrigger(&t, m); err != nil {
		return err
	}
	res.triggers = append(res.triggers, t)
	return nil
}
