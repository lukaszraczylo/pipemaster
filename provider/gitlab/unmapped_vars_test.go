package gitlab_test

import "testing"

func TestUnmappedPredefinedVariablesAreReported(t *testing.T) {
	_, rep := pipeline(t, `build:
  image: $CI_REGISTRY_IMAGE/builder:$CI_COMMIT_SHA
  tags: [$RUNNER_TAG]
  variables:
    LOCAL: mine
  script:
    - echo $CI_COMMIT_REF_SLUG ${CI_PROJECT_DIR}/out $LOCAL $[[ inputs.target ]]
    - echo $CI_COMMIT_SHA $$CI_NOT_A_VAR
  cache:
    key: $CI_JOB_NAME
    paths: [$CI_PROJECT_DIR/.cache]
  environment:
    name: review/$CI_ENVIRONMENT_SLUG
`)
	for _, want := range []string{"$CI_REGISTRY_IMAGE", "$CI_COMMIT_REF_SLUG", "$CI_PROJECT_DIR", "$CI_JOB_NAME", "$CI_ENVIRONMENT_SLUG", "$[[ inputs.target ]]"} {
		if !hasEntry(rep, "references "+want) {
			t.Errorf("no report entry names %s: %+v", want, rep.Entries)
		}
	}
	for _, not := range []string{"$CI_COMMIT_SHA", "$LOCAL", "$CI_NOT_A_VAR"} {
		if hasEntry(rep, "references "+not) {
			t.Errorf("%s is mapped, user-defined or escaped and must not be reported", not)
		}
	}
}

func TestUserDefinedCIVariableIsNotReported(t *testing.T) {
	_, rep := pipeline(t, `variables:
  CI_CUSTOM: x
build:
  script:
    - echo $CI_CUSTOM
`)
	if hasEntry(rep, "references $CI_CUSTOM") {
		t.Errorf("a variable the pipeline defines is not predefined: %+v", rep.Entries)
	}
}
