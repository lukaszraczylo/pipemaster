// Package glchild plans how the GitLab engine of the harness runs the child pipelines of a file. gitlab-ci-local
// 4.77.1 skips trigger jobs, so the harness runs each same-repository child itself and splices what the child did
// into the trigger job of the parent. Every rule below follows the GitLab documentation:
//
//   - https://docs.gitlab.com/ci/pipelines/downstream_pipelines/ : "In child pipelines, $CI_PIPELINE_SOURCE always has a
//     value of parent_pipeline"; "The child pipeline's configuration is composed of all configuration files merged
//     together" (up to three files); "The trigger job shows passed if the downstream pipeline is created successfully,
//     otherwise it shows failed"; "all jobs in a pipeline, including trigger jobs, inherit" default variables, which
//     inherit:variables can block; "Without these strategies, the parent pipeline completes before child pipelines
//     finish"; "a maximum depth of two levels of child pipelines".
//   - https://docs.gitlab.com/ci/yaml/#triggerforward : yaml_variables (default true) passes "both default variables
//     that jobs inherit, and variables defined directly in the trigger job's variables block"; pipeline_variables
//     (default false) passes "variables from manual pipeline runs, scheduled pipelines, and dotenv variables".
//   - https://docs.gitlab.com/ci/yaml/#triggerstrategy : with depend "The trigger job status shows failed, success,
//     or running, depending on the downstream pipeline status".
package glchild

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Source is the value of CI_PIPELINE_SOURCE in every child pipeline.
const Source = "parent_pipeline"

// MaxDepth is the number of levels of child pipelines that GitLab allows.
const MaxDepth = 2

// MaxIncludes is the number of files that one child pipeline can include.
const MaxIncludes = 3

// Reasons that a trigger cannot run on the harness. The engine maps each to a cited text.
const (
	ReasonProject  = "project"
	ReasonArtifact = "artifact"
	ReasonRemote   = "remote"
	ReasonTemplate = "template"
	ReasonGlob     = "glob"
	ReasonFiles    = "files"
	ReasonForm     = "form"
	ReasonInherit  = "inherit"
)

const (
	keyTrigger   = "trigger"
	keyInclude   = "include"
	keyVariables = "variables"
	keyScript    = "script"
	keyInherit   = "inherit"
	keyForward   = "forward"
	keyStrategy  = "strategy"
	keyExtends   = "extends"
	mergeKey     = "<<"
	maxInherit   = 8
	keyLocal     = "local"
	strategyDep  = "depend"
	strategyMirr = "mirror"
)

var reserved = map[string]bool{
	"stages": true, "variables": true, "default": true, "include": true, "workflow": true, "image": true,
	"services": true, "cache": true, "before_script": true, "after_script": true, "spec": true,
}

// Trigger is one trigger job that starts a child pipeline.
type Trigger struct {
	// Vars are the variables of the trigger job that the child receives, with the highest precedence.
	Vars   map[string]string
	Job    string
	Reason string
	Files  []string
	// Depend is true when the trigger job takes the status of the child.
	Depend bool
}

// Triggers lists the trigger jobs of a pipeline file in file order. pipelineVars are the variables that a run set by
// hand; the child gets them only when the trigger forwards pipeline variables.
func Triggers(doc []byte, pipelineVars map[string]string) ([]Trigger, error) {
	top, err := topMap(doc)
	if err != nil || top == nil {
		return nil, err
	}
	global := varMap(get(top, keyVariables))
	var out []Trigger
	for i := 0; i+1 < len(top.Content); i += 2 {
		name, job := top.Content[i].Value, top.Content[i+1]
		if reserved[name] || strings.HasPrefix(name, ".") || job.Kind != yaml.MappingNode {
			continue
		}
		trig := get(job, keyTrigger)
		if trig == nil {
			if inheritsTrigger(top, job, 0) {
				out = append(out, Trigger{Job: name, Reason: ReasonInherit})
			}
			continue
		}
		out = append(out, plan(name, job, trig, global, pipelineVars))
	}
	return out, nil
}

// inheritsTrigger reports whether a job gets its trigger key from a job it extends or from a YAML merge key.
func inheritsTrigger(top, job *yaml.Node, depth int) bool {
	if depth > maxInherit {
		return false
	}
	var from []*yaml.Node
	if ext := get(job, keyExtends); ext != nil {
		names := []*yaml.Node{ext}
		if ext.Kind == yaml.SequenceNode {
			names = ext.Content
		}
		for _, n := range names {
			from = append(from, get(top, n.Value))
		}
	}
	for i := 0; i+1 < len(job.Content); i += 2 {
		if job.Content[i].Value != mergeKey {
			continue
		}
		srcs := []*yaml.Node{job.Content[i+1]}
		if srcs[0].Kind == yaml.SequenceNode {
			srcs = srcs[0].Content
		}
		for _, s := range srcs {
			if s.Kind == yaml.AliasNode {
				s = s.Alias
			}
			from = append(from, s)
		}
	}
	return slices.ContainsFunc(from, func(n *yaml.Node) bool {
		return n != nil && n.Kind == yaml.MappingNode && (get(n, keyTrigger) != nil || inheritsTrigger(top, n, depth+1))
	})
}

func plan(name string, job, trig *yaml.Node, global, pipelineVars map[string]string) Trigger {
	t := Trigger{Job: name, Vars: map[string]string{}}
	if trig.Kind != yaml.MappingNode {
		t.Reason = ReasonProject
		return t
	}
	if s := get(trig, keyStrategy); s != nil {
		t.Depend = s.Value == strategyDep || s.Value == strategyMirr
	}
	if get(trig, "project") != nil {
		t.Reason = ReasonProject
		return t
	}
	t.Files, t.Reason = localFiles(get(trig, keyInclude))
	if t.Reason != "" {
		return t
	}
	fwd := get(trig, keyForward)
	if forwards(fwd, "yaml_variables", true) {
		maps.Copy(t.Vars, inherited(global, get(job, keyInherit)))
		maps.Copy(t.Vars, varMap(get(job, keyVariables)))
	}
	if forwards(fwd, "pipeline_variables", false) {
		maps.Copy(t.Vars, pipelineVars)
	}
	return t
}

// forwards reads a boolean of trigger:forward, with the documented default.
func forwards(fwd *yaml.Node, key string, def bool) bool {
	if v := get(fwd, key); v != nil {
		return v.Value == "true"
	}
	return def
}

// inherited applies inherit:variables to the default variables: false passes none, a list passes the named ones.
func inherited(global map[string]string, inherit *yaml.Node) map[string]string {
	v := get(inherit, keyVariables)
	switch {
	case v == nil:
		return global
	case v.Kind == yaml.ScalarNode && v.Value == "false":
		return nil
	case v.Kind == yaml.SequenceNode:
		out := map[string]string{}
		for _, n := range v.Content {
			if val, ok := global[n.Value]; ok {
				out[n.Value] = val
			}
		}
		return out
	}
	return global
}

func localFiles(inc *yaml.Node) ([]string, string) {
	if inc == nil {
		return nil, ReasonForm
	}
	items := []*yaml.Node{inc}
	if inc.Kind == yaml.SequenceNode {
		items = inc.Content
	}
	var files []string
	for _, it := range items {
		p := it.Value
		if it.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(it.Content); i += 2 {
				switch k := it.Content[i].Value; k {
				case keyLocal:
					p = it.Content[i+1].Value
				case "artifact", "job":
					return nil, ReasonArtifact
				case "remote":
					return nil, ReasonRemote
				case "template", "component":
					return nil, ReasonTemplate
				case "project", "file", "ref":
					return nil, ReasonProject
				default:
					return nil, ReasonForm
				}
			}
		} else if it.Kind != yaml.ScalarNode {
			return nil, ReasonForm
		}
		if strings.Contains(p, "://") {
			return nil, ReasonRemote
		}
		if strings.ContainsAny(p, "*?") {
			return nil, ReasonGlob
		}
		files = append(files, strings.TrimPrefix(path.Clean("/"+p), "/"))
	}
	if len(files) == 0 || len(files) > MaxIncludes {
		return nil, ReasonFiles
	}
	return files, ""
}

// Merge joins the files of one child pipeline the way GitLab merges included files: maps merge by key, and any
// other value of a later file replaces the earlier one. A single file passes through unchanged, and the merge keeps
// the order of the keys, the anchors and the tags of the files.
func Merge(files [][]byte) ([]byte, error) {
	if len(files) == 1 {
		return files[0], nil
	}
	var merged *yaml.Node
	for i, f := range files {
		top, err := topMap(f)
		if err != nil {
			return nil, fmt.Errorf("child file %d: %w", i+1, err)
		}
		switch {
		case top == nil:
		case merged == nil:
			merged = top
		default:
			mergeMaps(merged, top)
		}
	}
	if merged == nil {
		return nil, nil
	}
	return yaml.Marshal(merged)
}

// mergeMaps adds the keys of b to a in order. A key that both hold with a map value merges; any other value of b
// replaces the value of a in place.
func mergeMaps(a, b *yaml.Node) {
	for i := 0; i+1 < len(b.Content); i += 2 {
		k, v := b.Content[i], b.Content[i+1]
		at := slices.IndexFunc(pairs(a), func(p int) bool { return a.Content[p].Value == k.Value })
		if at < 0 {
			a.Content = append(a.Content, k, v)
			continue
		}
		p := pairs(a)[at]
		if a.Content[p+1].Kind == yaml.MappingNode && v.Kind == yaml.MappingNode {
			mergeMaps(a.Content[p+1], v)
			continue
		}
		a.Content[p+1] = v
	}
}

// pairs returns the index of each key of a mapping node.
func pairs(m *yaml.Node) []int {
	var out []int
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, i)
	}
	return out
}

// Outcome is what a child pipeline did.
type Outcome struct {
	Markers []string
	Exit    int
	// Created is false when GitLab creates no child pipeline, for example when its workflow:rules match nothing.
	Created bool
	Depend  bool
}

// Fails reports whether the trigger job fails: its child was not created, or its child failed under depend. Without
// depend the job passes once the child is created (https://docs.gitlab.com/ci/pipelines/downstream_pipelines/).
func (o Outcome) Fails() bool { return !o.Created || (o.Depend && o.Exit != 0) }

// Rewrite replaces each trigger job that has an outcome with a script job that prints the markers of its child and
// fails as the trigger job does. The job keeps its stage, needs, rules, when and allow_failure, so the engine
// decides whether and when it runs, which is when the child starts.
func Rewrite(doc []byte, outcomes map[string]Outcome) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return doc, nil
	}
	top := root.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		o, ok := outcomes[top.Content[i].Value]
		job := top.Content[i+1]
		if !ok || job.Kind != yaml.MappingNode || get(job, keyTrigger) == nil {
			continue
		}
		job.Content = dropKeys(job.Content, keyTrigger, keyScript)
		var lines []*yaml.Node
		for _, m := range o.Markers {
			lines = append(lines, str("printf '%s\\n' "+quote("MARK "+m)))
		}
		if o.Fails() {
			lines = append(lines, str("exit 1"))
		}
		if len(lines) == 0 {
			lines = append(lines, str("true"))
		}
		job.Content = append(job.Content, str(keyScript), &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: lines})
	}
	return yaml.Marshal(&root)
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

func dropKeys(content []*yaml.Node, keys ...string) []*yaml.Node {
	var out []*yaml.Node
	for i := 0; i+1 < len(content); i += 2 {
		if !slices.Contains(keys, content[i].Value) {
			out = append(out, content[i], content[i+1])
		}
	}
	return out
}

func topMap(doc []byte) (*yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	return root.Content[0], nil
}

func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// varMap reads a variables block: a value is a scalar or a mapping with a value key.
func varMap(n *yaml.Node) map[string]string {
	out := map[string]string{}
	if n == nil || n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		v := n.Content[i+1]
		if v.Kind == yaml.MappingNode {
			v = get(v, "value")
		}
		if v != nil {
			out[n.Content[i].Value] = v.Value
		}
	}
	return out
}

var listStart = regexp.MustCompile(`(?m)^\[$`)

// Running reads the output of `gitlab-ci-local --list-json` and returns the jobs that start by themselves for the
// run: the ones that the rules did not drop (when: never) and that need no operator (when: manual).
func Running(out string) (map[string]bool, error) {
	at := listStart.FindStringIndex(out)
	if at == nil {
		return nil, fmt.Errorf("no job list in the output of gitlab-ci-local")
	}
	var jobs []struct {
		Name string `json:"name"`
		When string `json:"when"`
	}
	if err := json.NewDecoder(strings.NewReader(out[at[0]:])).Decode(&jobs); err != nil {
		return nil, fmt.Errorf("read the job list: %w", err)
	}
	run := map[string]bool{}
	for _, j := range jobs {
		run[j.Name] = j.When != "never" && j.When != "manual"
	}
	return run, nil
}
