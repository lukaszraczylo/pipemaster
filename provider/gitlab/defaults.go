package gitlab

import (
	"slices"

	"go.yaml.in/yaml/v3"
)

var (
	// defaultStages is GitLab's stage list when the file declares none.
	defaultStages = []string{"build", "test", "deploy"}

	// defaultKeys are the keywords `default:` may set, in a fixed order.
	defaultKeys = []string{
		"after_script", "artifacts", "before_script", "cache", "hooks", "id_tokens",
		"image", "interruptible", "retry", "services", "tags", "timeout",
	}

	// deprecatedGlobals act as defaults when written at the top level; `default:` wins over them.
	deprecatedGlobals = map[string]bool{"image": true, "services": true, "cache": true, "before_script": true, "after_script": true}
)

// defaults collects the effective default value of every inheritable keyword.
func (s *state) defaults(root *yaml.Node) ([]entry, error) {
	d := get(root, "default")
	if d != nil && d.Kind != yaml.MappingNode {
		return nil, s.errf(d, "default must be a mapping")
	}
	var out []entry
	for _, k := range defaultKeys {
		if v := get(d, k); v != nil {
			out = append(out, entry{name: k, val: v})
		} else if v := get(root, k); v != nil && deprecatedGlobals[k] {
			out = append(out, entry{name: k, val: v})
		}
	}
	return out, nil
}

// applyDefaults copies each default keyword the job lacks into the job, honouring inherit:default.
func (s *state) applyDefaults(job *yaml.Node, def []entry) (*yaml.Node, error) {
	allowed, err := s.inheritedDefaults(job)
	if err != nil {
		return nil, err
	}
	out := s.stamp(&yaml.Node{Kind: yaml.MappingNode, Tag: job.Tag, Style: job.Style, Line: job.Line, Column: job.Column}, s.org[job])
	for _, e := range entries(job) {
		// A null on a default keyword only opts out of the default; any other null unsets what a parent job set, so it stays.
		if !isNull(e.val) || !slices.Contains(defaultKeys, e.name) {
			out.Content = append(out.Content, e.key, e.val)
		}
	}
	for _, e := range def {
		if get(job, e.name) != nil || (allowed != nil && !allowed[e.name]) {
			continue
		}
		key := s.stamp(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.name, Line: job.Line}, s.org[job])
		out.Content = append(out.Content, key, e.val)
	}
	return out, nil
}

// inheritedDefaults returns nil for "all defaults" or the set of keywords the job inherits.
func (s *state) inheritedDefaults(job *yaml.Node) (map[string]bool, error) {
	inh := get(job, "inherit")
	if inh == nil {
		return nil, nil
	}
	if inh.Kind != yaml.MappingNode {
		return nil, s.errf(inh, "inherit must be a mapping")
	}
	d := get(inh, "default")
	if d == nil {
		return nil, nil
	}
	if b, ok := boolNode(d); ok {
		if b {
			return nil, nil
		}
		return map[string]bool{}, nil
	}
	l, ok := stringList(d)
	if !ok {
		return nil, s.errf(d, "inherit:default must be true, false or a list of keywords")
	}
	set := map[string]bool{}
	for _, k := range l {
		set[k] = true
	}
	return set, nil
}

// takeDefaults consumes `default:` and the deprecated globals: applyDefaults expands them into every job, so
// keeping them in Ext would duplicate what the IR already holds. Only default keys no job can inherit stay.
func (s *state) takeDefaults(r *reader) {
	for k := range deprecatedGlobals {
		r.take(k)
	}
	d := r.take("default")
	if d == nil || d.Kind != yaml.MappingNode {
		return
	}
	rest := map[string]any{}
	for _, e := range entries(d) {
		if slices.Contains(defaultKeys, e.name) {
			continue
		}
		if v, err := s.plain(e.val); err == nil {
			rest[e.name] = v
		}
	}
	if len(rest) > 0 {
		r.keep("default", rest)
	}
}
