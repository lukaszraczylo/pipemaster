//go:build behave

package behave

import (
	"slices"

	"go.yaml.in/yaml/v3"
)

// glReserved are the top-level keys of a GitLab file that are no job.
var glReserved = map[string]bool{
	"stages": true, "variables": true, "default": true, "include": true, "workflow": true, "image": true,
	"services": true, "cache": true, "before_script": true, "after_script": true,
}

// glJobKeysKeepingRules are the keys that decide on their own whether a job joins a pipeline.
var glJobKeysKeepingRules = []string{"rules", "extends", "trigger"}

// glMRRef is the only/except keyword for merge request pipelines
// (https://docs.gitlab.com/ci/yaml/deprecated_keywords/, only:refs / except:refs).
const glMRRef = "merge_requests"

// mergeRequestJobs applies GitLab's own pipeline-creation rule to a file for a merge request event: without
// workflow:rules, a job joins a merge request pipeline only when its rules match CI_PIPELINE_SOURCE ==
// "merge_request_event" (https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/, Prerequisites). A job with
// no rules of its own is therefore dropped. It returns the file and whether any job remains.
func mergeRequestJobs(raw []byte) ([]byte, bool, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, false, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return raw, true, nil
	}
	top := root.Content[0]
	remaining := false
	for i := 0; i+1 < len(top.Content); i += 2 {
		name, job := top.Content[i].Value, top.Content[i+1]
		if name == "workflow" {
			return raw, true, nil
		}
		if glReserved[name] || name == "" || name[0] == '.' || job.Kind != yaml.MappingNode {
			continue
		}
		if hasKey(job, glJobKeysKeepingRules) {
			remaining = true
			continue
		}
		if hasKey(job, []string{"only", "except"}) && joinsMergeRequest(job) {
			remaining = true
			continue
		}
		dropKeys(job, "only", "except")
		never := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "when"}, {Kind: yaml.ScalarNode, Tag: "!!str", Value: "never"},
		}}
		job.Content = append(job.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "rules"},
			&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{never}})
	}
	out, err := yaml.Marshal(&root)
	return out, remaining, err
}

func hasKey(m *yaml.Node, keys []string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		for _, k := range keys {
			if m.Content[i].Value == k {
				return true
			}
		}
	}
	return false
}

// joinsMergeRequest decides a job with only or except and no rules. Without only, a job takes the default refs of
// branches and tags, so it joins no merge request pipeline. With only, it joins when only lists merge_requests and
// except does not.
func joinsMergeRequest(job *yaml.Node) bool {
	return refsInclude(keyValue(job, "only")) && !refsInclude(keyValue(job, "except"))
}

// refsInclude reads the refs of an only or except value, a list or a map with a refs list, for merge_requests.
func refsInclude(v *yaml.Node) bool {
	if v == nil {
		return false
	}
	if v.Kind == yaml.MappingNode {
		v = keyValue(v, "refs")
		if v == nil {
			return false
		}
	}
	if v.Kind == yaml.ScalarNode {
		return v.Value == glMRRef
	}
	for _, n := range v.Content {
		if n.Value == glMRRef {
			return true
		}
	}
	return false
}

func keyValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// dropKeys removes keys from a mapping, because GitLab refuses a job that has both rules and only or except.
func dropKeys(m *yaml.Node, keys ...string) {
	kept := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !slices.Contains(keys, m.Content[i].Value) {
			kept = append(kept, m.Content[i], m.Content[i+1])
		}
	}
	m.Content = kept
}
