package gitlab_test

import (
	"strings"
	"testing"
)

func TestSingleQuotedAndHeredocNamesStayText(t *testing.T) {
	doc := "build:\n  script:\n    - echo '$CI_COMMIT_SHA' $CI_COMMIT_SHA\n    - |\n      cat <<'EOT'\n      $CI_COMMIT_SHA\n      EOT\n"
	p, _ := parseFiles(t, map[string]string{rootFile: doc})
	got := strings.Join(p.Pipelines[0].Jobs[0].Steps[0].Script, "\n")
	for _, want := range []string{"echo '$CI_COMMIT_SHA' ${{ sha }}", "$CI_COMMIT_SHA\nEOT"} {
		if !strings.Contains(got, want) {
			t.Errorf("script lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "${{ sha }}") != 1 {
		t.Errorf("only the unquoted read becomes a reference:\n%s", got)
	}
}

func TestEventDependentNamesStayText(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{rootFile: "build:\n  script:\n    - echo $CI_COMMIT_BRANCH $CI_COMMIT_REF_NAME $CI_PIPELINE_SOURCE\n"})
	got := p.Pipelines[0].Jobs[0].Steps[0].Script[0]
	if strings.Contains(got, "${{") {
		t.Errorf("a name with an event-dependent meaning became a reference: %q", got)
	}
}
