package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestJobConditionWithAnUnspellableRefIsAFailSafe(t *testing.T) {
	j := scriptJob("test", "make")
	j.If = eqRef("event", "push")
	d := scriptJob("deploy", "make deploy")
	d.Needs = []ir.Need{{JobID: "test"}}
	p := project(j, d)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, rep := emit(t, p)
	doc := pipelineDoc(t, files)
	for _, s := range list(jobDoc(t, doc, "test")["plan"]) {
		if dict(s)["trigger"] == true {
			t.Fatalf("the job triggers though its condition has no spelling: %v", s)
		}
	}
	if strings.Contains(files[".concourse/ci.yml"], "exit 0 # pipemaster:guard") {
		t.Fatalf("a vacuous task stands for the job:\n%s", files[".concourse/ci.yml"])
	}
	if !hasFeatureEntry(rep, string(caps.JobIf), "") {
		t.Fatalf("no JobIf entry: %v", rep.Entries)
	}
}

const tryPipeline = `resources:
  - name: repo
    type: git
    source: {uri: https://example.com/r.git, branch: main}
  - name: chat
    type: registry-image
    source: {repository: acme/notify}
jobs:
  - name: build
    plan:
      - get: repo
        trigger: true
      - try:
          task: lint
          file: repo/ci/lint.yml
      - try:
          put: chat
          params: {text: done}
`

func TestTryOnFileTasksAndResourceStepsIsKept(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": tryPipeline})
	files, rep := emit(t, p)
	plan := list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])
	var tries int
	for _, s := range plan {
		if dict(s)["try"] != nil {
			tries++
		}
	}
	if tries != 2 {
		t.Fatalf("%d try steps in the plan, want 2:\n%s", tries, files[".concourse/ci.yml"])
	}
	if !hasFeatureEntry(rep, string(caps.StepContinueOnError), "") {
		t.Fatalf("no entry for the try wrappers: %v", rep.Entries)
	}
}

func TestEnvironmentOnATriggeredJobComesBack(t *testing.T) {
	for name, env := range map[string]*ir.Environment{
		"plain":    {Name: "prod", URL: "https://example.com"},
		"approval": {Name: "prod", Approval: true},
	} {
		j := scriptJob("deploy", "make deploy")
		j.Environment = env
		p := project(j)
		p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
		files, _ := emit(t, p)
		back, rep := reparse(t, files)
		got := back.Pipelines[0].Jobs[0]
		if got.Environment == nil || *got.Environment != *env || got.Manual {
			t.Fatalf("%s: job came back as manual=%v env=%+v", name, got.Manual, got.Environment)
		}
		if hasFeatureEntry(rep, string(caps.JobManual), "marker ignored") {
			t.Fatalf("%s: an unedited marker was ignored: %v", name, rep.Entries)
		}
	}
}
