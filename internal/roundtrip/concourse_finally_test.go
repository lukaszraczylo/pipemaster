package roundtrip_test

import (
	"testing"
)

// A chain keeps its finally jobs through Concourse, which places them in the hooks of every job of the chain.
const gitlabChainFinally = `image: alpine:3.20
stages: [build, test, after]
build:
  stage: build
  script: [echo build]
unit:
  stage: test
  script: [echo unit]
recover:
  stage: after
  when: on_failure
  script: [echo recover]
cleanup:
  stage: after
  when: always
  script: [echo cleanup]
`

func TestChainFinallyJobsSurviveTheConcourseRoundTrip(t *testing.T) {
	code, out, msg := runOut("roundtrip", writeGitlab(t, gitlabChainFinally), "--via", "concourse")
	if code != 0 || out != "no differences\n" {
		t.Fatalf("code=%d out=%q\n%s", code, out, msg)
	}
}
