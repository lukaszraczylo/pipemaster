package roundtrip_test

import (
	"strings"
	"testing"
)

const failureNotifyWorkflow = `name: ci
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
  test:
    runs-on: ubuntu-latest
    steps:
      - run: make test
  notify:
    needs: [build, test]
    if: failure()
    runs-on: ubuntu-latest
    steps:
      - run: echo failed
`

// A job held for an untranslatable condition must stay manual when its Concourse file is edited.
func TestEditedHeldConcourseJobIsManualInGHA(t *testing.T) {
	src, ccDir, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, src, ".github/workflows/ci.yml", failureNotifyWorkflow)
	if code, msg := runCLI("convert", src, "--to", "concourse", "-o", ccDir); code != 0 {
		t.Fatalf("convert to concourse = %d\n%s", code, msg)
	}
	file := ".concourse/ci.yml"
	edited := strings.Replace(readTree(t, ccDir, file), "echo failed", "echo failed; echo more", 1)
	writeTree(t, ccDir, file, edited)
	code, msg := runCLI("convert", ccDir, "--from", "concourse", "--to", "gha", "-o", back, "--force")
	if code != 0 {
		t.Fatalf("convert --force = %d\n%s", code, msg)
	}
	got := readTree(t, back, ".github/workflows/ci.yml")
	if !strings.Contains(got, "if: github.event_name == 'workflow_dispatch'") {
		t.Errorf("the notify job runs with no gate:\n%s", got)
	}
	if !strings.Contains(msg, "the job stays manual") {
		t.Errorf("the report does not say the job stays manual:\n%s", msg)
	}
}

const unreadableBranchPipeline = `resources:
  - name: app
    type: git
    source: {uri: ((git-uri)), branch: ((branch))}
jobs:
  - name: build
    plan:
      - get: app
        trigger: true
      - task: unit
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: ubuntu}}
          inputs: [{name: app}]
          run: {path: sh, args: [-ec, "make"]}
`

// An unreadable trigger becomes an API trigger; the GHA file names repository_dispatch for it, and the return trip
// must not keep that name as an event of the pipeline.
func TestUnreadableConcourseTriggerReturnsFromGHA(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src, ".concourse/ci.yml", unreadableBranchPipeline)
	for _, via := range []string{"gha", "gitlab"} {
		if code, msg := runCLI("roundtrip", src, "--via", via); code != 0 {
			t.Errorf("roundtrip via %s = %d\n%s", via, code, msg)
		}
	}
}
