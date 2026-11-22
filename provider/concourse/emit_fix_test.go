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

const dailyPipeline = `resources:
  - name: daily
    type: time
    source: {interval: 24h}
  - name: repo
    type: git
    source: {uri: https://example.com/r.git, branch: main}
jobs:
  - name: scan
    plan:
      - get: daily
        trigger: true
      - get: repo
      - task: scan
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          inputs: [{name: repo}]
          run: {path: sh, args: [-ec, "echo scan"]}
`

func TestOwnTimeSourceIsRestoredVerbatim(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": dailyPipeline})
	files, _ := emit(t, p)
	src := resourceDoc(pipelineDoc(t, files), "daily")
	if src == nil || dict(src["source"])["interval"] != "24h" {
		t.Fatalf("time source = %v", src)
	}
}

func TestMidnightCronIsNotAnEmptySource(t *testing.T) {
	p := project(scriptJob("nightly", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule, Cron: "0 0 * * *"}}
	files, _ := emit(t, p)
	var tm map[string]any
	for _, r := range list(pipelineDoc(t, files)["resources"]) {
		if dict(r)["type"] == "time" {
			tm = dict(r)
		}
	}
	if tm == nil || len(dict(tm["source"])) == 0 {
		t.Fatalf("time resource = %v", tm)
	}
	back, _ := reparse(t, files)
	var cron string
	for _, tr := range back.Pipelines[0].Triggers {
		if tr.Kind == ir.TriggerSchedule {
			cron = tr.Cron
		}
	}
	if cron != "0 0 * * *" {
		t.Fatalf("cron came back as %q", cron)
	}
}

func TestPipelineWithNoJobsEmitsAValidPlaceholder(t *testing.T) {
	p := project()
	files, rep := emit(t, p)
	if _, ok := files[".concourse/ci.yml"]; !ok {
		t.Fatalf("no file for the empty pipeline: %v", keys(files))
	}
	if !hasFeatureEntry(rep, string(caps.ExtPassthrough), "no jobs") {
		t.Fatalf("no entry for the empty pipeline: %v", rep.Entries)
	}
	back, brep := reparse(t, files)
	if got := back.Pipelines[0]; len(got.Jobs) != 0 {
		t.Fatalf("the placeholder job came back: %v", got.Jobs)
	}
	if hasFeatureEntry(brep, string(caps.ExtPassthrough), "marker ignored") {
		t.Fatalf("an unedited marker was ignored: %v", brep.Entries)
	}
}

func TestEditedPlaceholderJobStaysAJob(t *testing.T) {
	files, _ := emit(t, project())
	src := files[".concourse/ci.yml"]
	edited := strings.Replace(src, `- "true"`, "- echo edited", 1)
	if edited == src {
		t.Fatalf("placeholder script changed shape:\n%s", src)
	}
	back, rep := parseFiles(t, map[string]string{"ci.yml": edited})
	if len(back.Pipelines[0].Jobs) != 1 || !hasFeatureEntry(rep, string(caps.ExtPassthrough), "marker ignored") {
		t.Fatalf("jobs=%v entries=%v", back.Pipelines[0].Jobs, rep.Entries)
	}
}
