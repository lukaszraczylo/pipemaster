package gitlab_test

import "testing"

func TestSemanticKeysParkedInExtAreNamedInTheReport(t *testing.T) {
	_, rep := pipeline(t, `workflow:
  name: $PIPELINE_NAME
  auto_cancel:
    on_new_commit: interruptible
  rules:
    - if: $CI_COMMIT_BRANCH == "main"
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
      variables:
        DEPLOY: "no"
variables:
  GLOBAL: x
test:
  script: [make]
  inherit:
    variables: false
  artifacts:
    when: on_failure
    paths: [out/]
  environment:
    name: review/x
    action: stop
`)
	for _, want := range []string{
		"environment:action", "artifacts:when", "inherit:variables",
		"workflow:name", "workflow:auto_cancel", "workflow:rules variables",
	} {
		if !hasEntry(rep, want) {
			t.Errorf("no report entry names %q: %+v", want, rep.Entries)
		}
	}
}
