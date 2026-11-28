//go:build behave

package behave

import (
	"strings"
	"testing"
)

func TestMergeRequestJobs(t *testing.T) {
	for _, c := range []struct {
		name, in string
		active   bool
		never    int
	}{
		{"rule-less job is dropped", "a:\n  script: [x]\n", false, 1},
		{"merge request rule stays", "a:\n  script: [x]\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"merge_request_event\"\n", true, 0},
		{"mixed", "a:\n  script: [x]\nb:\n  script: [y]\n  rules: [{if: '$CI_COMMIT_BRANCH'}]\n", true, 1},
		{"workflow decides", "workflow:\n  rules: [{when: always}]\na:\n  script: [x]\n", true, 0},
		{"only merge_requests stays", "a:\n  script: [x]\n  only: [merge_requests]\n", true, 0},
		{"only refs map with merge_requests stays", "a:\n  script: [x]\n  only:\n    refs: [merge_requests]\n", true, 0},
		{"only branches is dropped", "a:\n  script: [x]\n  only: [branches]\n", false, 1},
		{"except alone is dropped", "a:\n  script: [x]\n  except: [tags]\n", false, 1},
		{"except merge_requests is dropped", "a:\n  script: [x]\n  only: [merge_requests]\n  except: [merge_requests]\n", false, 1},
		{"pages is a job", "pages:\n  script: [x]\n", false, 1},
		{"hidden and reserved keys are no jobs", "stages: [t]\n.tpl:\n  script: [x]\n", false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, active, err := mergeRequestJobs([]byte(c.in))
			if err != nil || active != c.active || strings.Count(string(out), "when: never") != c.never {
				t.Errorf("active=%v never=%d err=%v\n%s", active, strings.Count(string(out), "when: never"), err, out)
			}
		})
	}
}
