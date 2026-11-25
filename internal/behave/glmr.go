//go:build behave

package behave

import (
	"go.yaml.in/yaml/v3"
)

// glReserved are the top-level keys of a GitLab file that are no job.
var glReserved = map[string]bool{
	"stages": true, "variables": true, "default": true, "include": true, "workflow": true, "image": true,
	"services": true, "cache": true, "before_script": true, "after_script": true, "pages": false,
}

// glJobKeysKeepingRules are the keys that decide on their own whether a job joins a pipeline.
var glJobKeysKeepingRules = []string{"rules", "only", "except", "extends", "trigger"}

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
