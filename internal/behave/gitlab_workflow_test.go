//go:build behave

package behave

import "testing"

func TestWorkflowAdmits(t *testing.T) {
	const branchOnly = "workflow:\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"push\" && $CI_COMMIT_BRANCH\n"
	push := map[string]string{"CI_PIPELINE_SOURCE": "push", "CI_COMMIT_BRANCH": "main"}
	tag := map[string]string{"CI_PIPELINE_SOURCE": "push", "CI_COMMIT_BRANCH": "", "CI_COMMIT_TAG": "v1"}
	mr := map[string]string{"CI_PIPELINE_SOURCE": "merge_request_event", "CI_COMMIT_BRANCH": ""}
	for name, tc := range map[string]struct {
		vars map[string]string
		file string
		want bool
	}{
		"no workflow admits every run":     {tag, "a:\n  script: [x]\n", true},
		"branch push is admitted":          {push, branchOnly, true},
		"tag push is not admitted":         {tag, branchOnly, false},
		"merge request is not admitted":    {mr, branchOnly, false},
		"when never stops a matching rule": {tag, "workflow:\n  rules:\n    - if: $CI_COMMIT_TAG\n      when: never\n    - when: always\n", false},
		"a rule without if matches":        {tag, "workflow:\n  rules:\n    - when: always\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := workflowAdmits([]byte(tc.file), tc.vars)
			if err != nil || got != tc.want {
				t.Fatalf("workflowAdmits = %v, %v, want %v", got, err, tc.want)
			}
		})
	}
}
