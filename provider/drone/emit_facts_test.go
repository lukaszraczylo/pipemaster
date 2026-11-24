package drone_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

// branchJob is a job whose only step runs when cond (a GitLab condition) holds.
func branchJob(t *testing.T, cond string) (text string, caveat, excluded bool) {
	t.Helper()
	x, err := gitlab.Parse(cond)
	if err != nil {
		t.Fatalf("parse %q: %v", cond, err)
	}
	st := gstep("deploy", "make")
	st.If = x
	text, rep := emitText(t, gproject("gitlab", gjob("deploy", st)))
	for _, en := range rep.Entries {
		caveat = caveat || strings.Contains(en.Detail, "the source may not")
		excluded = excluded || strings.Contains(en.Detail, "leaves pull requests out")
	}
	return text, caveat, excluded
}

// A GitLab branch test leaves CI_COMMIT_BRANCH unset in a merge request pipeline, and Drone matches the branch
// filter against the target branch there, so the filter must exclude pull requests.
func TestGitLabBranchTestExcludesPullRequestsInDrone(t *testing.T) {
	text, caveat, excluded := branchJob(t, `$CI_COMMIT_BRANCH == "main"`)
	if !strings.Contains(text, "exclude") || !strings.Contains(text, "pull_request") {
		t.Errorf("the branch filter must exclude pull requests:\n%s", text)
	}
	if caveat {
		t.Errorf("the pull request caveat must go once the filter excludes them:\n%s", text)
	}
	if !excluded {
		t.Errorf("the exclusion must be reported:\n%s", text)
	}
}

func TestGitLabTagTestNeedsNoExclusionInDrone(t *testing.T) {
	text, _, _ := branchJob(t, `$CI_COMMIT_TAG == "v1"`)
	if strings.Contains(text, "pull_request") {
		t.Errorf("a tag test needs no pull request exclusion:\n%s", text)
	}
}
