package gitlab_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const stagedPipeline = `
stages: [build, test, deploy]
compile:
  stage: build
  script: [make]
  artifacts:
    paths: [bin/]
lint:
  stage: build
  script: [lint]
unit:
  stage: test
  script: [test]
deploy-it:
  stage: deploy
  script: [deploy]
`

func TestStagesImplicitNeeds(t *testing.T) {
	pl, _ := pipeline(t, stagedPipeline)
	if got := needIDs(job(t, pl, "compile")); len(got) != 0 {
		t.Errorf("first stage needs = %v", got)
	}
	if got := needIDs(job(t, pl, "unit")); !reflect.DeepEqual(got, []string{"compile", "lint"}) {
		t.Errorf("unit needs = %v", got)
	}
	d := job(t, pl, "deploy-it")
	if got := needIDs(d); !reflect.DeepEqual(got, []string{"compile", "lint", "unit"}) {
		t.Errorf("deploy needs = %v", got)
	}
	art := map[string]bool{}
	for _, n := range d.Needs {
		art[n.JobID] = n.Artifacts
	}
	if !art["compile"] || art["lint"] || art["unit"] {
		t.Errorf("Artifacts must follow what the needed job declares: %v", art)
	}
	if ext(d.Ext)["_derived_needs"] != true {
		t.Errorf("implicit needs must be flagged: %v", d.Ext)
	}
	if ext(pl.Ext)["stages"] == nil {
		t.Errorf("stages must stay in pipeline Ext")
	}
}

func TestDefaultStagesAndTestStage(t *testing.T) {
	pl, _ := pipeline(t, `
b:
  stage: build
  script: [x]
t:
  script: [x]
d:
  stage: deploy
  script: [x]
pre:
  stage: .pre
  script: [x]
post:
  stage: .post
  script: [x]
`)
	if got := needIDs(job(t, pl, "t")); !reflect.DeepEqual(got, []string{"b", "pre"}) {
		t.Errorf("a job without stage is in test: needs = %v", got)
	}
	if got := needIDs(job(t, pl, "post")); !reflect.DeepEqual(got, []string{"b", "d", "pre", "t"}) {
		t.Errorf(".post runs last: needs = %v", got)
	}
	if got := needIDs(job(t, pl, "b")); !reflect.DeepEqual(got, []string{"pre"}) {
		t.Errorf(".pre runs first: needs = %v", got)
	}
	if ext(job(t, pl, "t").Ext)["stage"] != nil {
		t.Errorf("an implicit stage must not be written to Ext")
	}
}

func TestNeedsEmptyList(t *testing.T) {
	pl, _ := pipeline(t, stagedPipeline+"\nfree:\n  stage: deploy\n  needs: []\n  script: [x]\n")
	f := job(t, pl, "free")
	if len(f.Needs) != 0 {
		t.Errorf("needs: [] means no dependencies: %v", f.Needs)
	}
	if ext(f.Ext)["_derived_needs"] != nil {
		t.Errorf("explicit needs are not derived")
	}
}

func TestExplicitNeeds(t *testing.T) {
	pl, _ := pipeline(t, stagedPipeline+`
picky:
  stage: deploy
  script: [x]
  needs:
    - compile
    - job: lint
      artifacts: false
    - job: unit
      optional: true
    - job: elsewhere
      project: grp/other
      ref: main
`)
	j := job(t, pl, "picky")
	if got := needIDs(j); !reflect.DeepEqual(got, []string{"compile", "lint", "unit"}) {
		t.Fatalf("needs = %v", got)
	}
	byID := map[string]bool{}
	opt := map[string]bool{}
	for _, n := range j.Needs {
		byID[n.JobID] = n.Artifacts
		opt[n.JobID] = n.Optional
	}
	if !byID["compile"] || byID["lint"] || !opt["unit"] || opt["compile"] {
		t.Errorf("need fields: artifacts=%v optional=%v", byID, opt)
	}
	if ext, _ := ext(j.Ext)["needs"].([]any); len(ext) != 1 {
		t.Errorf("cross-project needs stay in Ext: %v", j.Ext)
	}
}

func TestDependenciesNarrowArtifacts(t *testing.T) {
	pl, _ := pipeline(t, `
stages: [build, test]
a:
  stage: build
  script: [x]
  artifacts: {paths: [a]}
b:
  stage: build
  script: [x]
  artifacts: {paths: [b]}
c:
  stage: test
  script: [x]
  dependencies: [a]
`)
	c := job(t, pl, "c")
	got := map[string]bool{}
	for _, n := range c.Needs {
		got[n.JobID] = n.Artifacts
	}
	if !got["a"] || got["b"] {
		t.Errorf("dependencies narrow artifact passing: %v", got)
	}
	if ext(c.Ext)["dependencies"] == nil {
		t.Errorf("dependencies stays in Ext")
	}
}

func TestFinallyJobsAreNotImplicitNeeds(t *testing.T) {
	pl, _ := pipeline(t, `
stages: [build, test, deploy]
b:
  stage: build
  script: [x]
cleanup:
  stage: test
  when: always
  script: [x]
d:
  stage: deploy
  script: [x]
`)
	if got := needIDs(job(t, pl, "d")); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("d needs = %v", got)
	}
	if got := needIDs(job(t, pl, "cleanup")); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("cleanup needs = %v", got)
	}
}

func TestUnknownStageIsError(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "stages: [a]\nj:\n  stage: nope\n  script: [x]\n"})
	if !strings.Contains(err.Error(), rootFile+":2") || !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v", err)
	}
}

func TestBrokenDAGErrors(t *testing.T) {
	cases := map[string]struct{ yml, want string }{
		"missing": {"a:\n  script: [x]\n  needs: [ghost]\n", ".gitlab-ci.yml:1"},
		"cycle":   {"a:\n  script: [x]\n  needs: [b]\nb:\n  script: [x]\n  needs: [a]\n", "needs cycle"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := parseErr(t, map[string]string{rootFile: c.yml})
			if !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), rootFile+":") {
				t.Errorf("error = %v, want %q with file:line", err, c.want)
			}
		})
	}
}

func TestJobNameWithSlashInError(t *testing.T) {
	err := parseErr(t, map[string]string{rootFile: "\"build/linux x\":\n  script: [x]\n  needs: [ghost]\n"})
	if !strings.Contains(err.Error(), rootFile+":1") {
		t.Errorf("error = %v", err)
	}
}

func TestOptionalMissingNeedIsAllowed(t *testing.T) {
	pl, _ := pipeline(t, "a:\n  script: [x]\n  needs:\n    - job: ghost\n      optional: true\n")
	if len(job(t, pl, "a").Needs) != 1 {
		t.Errorf("an optional need on a missing job is kept")
	}
}

func TestDotenvOnlyJobDeclaresArtifacts(t *testing.T) {
	pl, _ := pipeline(t, `
stages: [build, test]
env:
  stage: build
  script: [echo A=1 > build.env]
  artifacts:
    reports:
      dotenv: build.env
junit:
  stage: build
  script: [x]
  artifacts:
    reports:
      junit: r.xml
use:
  stage: test
  script: [x]
`)
	got := map[string]bool{}
	for _, n := range job(t, pl, "use").Needs {
		got[n.JobID] = n.Artifacts
	}
	if !got["env"] || got["junit"] {
		t.Errorf("dotenv reports are passed to later jobs, other reports are not: %v", got)
	}
}

func TestNeedsExtrasKeepOnlyUnconsumed(t *testing.T) {
	pl, _ := pipeline(t, `
stages: [build, test]
a:
  stage: build
  script: [x]
b:
  stage: test
  script: [x]
  needs:
    - job: a
      artifacts: false
      parallel:
        matrix: [{X: [1]}]
    - job: other
      project: grp/p
`)
	b := job(t, pl, "b")
	x := ext(b.Ext)
	extra, _ := x["needs_extra"].(map[string]any)
	if !reflect.DeepEqual(extra, map[string]any{"a": map[string]any{"parallel": map[string]any{"matrix": []any{map[string]any{"X": []any{1}}}}}}) {
		t.Errorf("needs_extra = %v", x["needs_extra"])
	}
	raw, _ := x["needs"].([]any)
	if len(raw) != 1 {
		t.Errorf("only the cross-project entry stays raw in needs: %v", raw)
	}
}

func TestEmitNeedsArtifactsWithoutProducerArtifactsIsLost(t *testing.T) {
	step := []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"x"}}}
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: []*ir.Job{
		{ID: "a", Steps: step},
		{ID: "b", Steps: step, Needs: []ir.Need{{JobID: "a", Artifacts: true}}},
		{ID: "c", Steps: step, Needs: []ir.Need{{JobID: "a"}}},
	}}}}
	_, rep := emitFiles(t, p)
	var got []string
	for _, e := range rep.Entries {
		if e.Feature == caps.JobNeedsArtifacts {
			got = append(got, e.IRPath+" "+e.Level.String())
		}
	}
	if want := []string{"pipeline/ci/job/b lost"}; !reflect.DeepEqual(got, want) {
		t.Errorf("needs.artifacts entries = %v, want %v", got, want)
	}
}

func TestEmitManualFinallyJobIsLost(t *testing.T) {
	step := []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"x"}}}
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci",
		Jobs:    []*ir.Job{{ID: "a", Steps: step}},
		Finally: []*ir.Job{{ID: "f", Steps: step, Manual: true}, {ID: "g", Steps: step}},
	}}}
	_, rep := emitFiles(t, p)
	var got []string
	for _, e := range rep.Entries {
		if e.Feature == caps.PipelineFinally {
			got = append(got, e.IRPath+" "+e.Level.String())
		}
	}
	if want := []string{"pipeline/ci/job/f lost"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pipeline.finally entries = %v, want %v", got, want)
	}
}
