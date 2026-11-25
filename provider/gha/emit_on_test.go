package gha_test

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func emitWith(t *testing.T, mutate func(*ir.Pipeline)) (string, *report.Report) {
	t.Helper()
	p := baseProject(t)
	mutate(p.Pipelines[0])
	files, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	data := files[".github/workflows/ci.yml"]
	if err := validate.File("gha", "ci.yml", data); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	lintFileIgnoring(t, "ci.yml", data, constantIf)
	return string(data), rep
}

func inc(ss ...string) ir.Filter { return ir.Filter{Include: ss} }

const trigPath = "pipeline/ci/trigger/"

func TestEmitPushTagPairs(t *testing.T) {
	cases := []struct {
		lost     map[string]caps.Feature
		name     string
		triggers []ir.Trigger
		want     []string
	}{
		{name: "unfiltered push beside tags", triggers: []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag, Tags: inc("v*")}},
			want: []string{"branches:", "'**'", "- v*"}},
		{name: "unfiltered tag beside branches", triggers: []ir.Trigger{{Kind: ir.TriggerPush, Branches: inc("main")}, {Kind: ir.TriggerTag}},
			want: []string{"- main", "tags:", "'**'"}},
		{name: "unfiltered tag alone", triggers: []ir.Trigger{{Kind: ir.TriggerTag}}, want: []string{"tags:", "'**'"}},
		{name: "paths differ", triggers: []ir.Trigger{{Kind: ir.TriggerPush, Branches: inc("main"), Paths: inc("a/**")}, {Kind: ir.TriggerTag, Tags: inc("v*"), Paths: inc("b/**")}},
			want: []string{"a/**"}, lost: map[string]caps.Feature{trigPath + "1": caps.TriggerFilterPaths}},
		{name: "push tags cannot be stated", triggers: []ir.Trigger{{Kind: ir.TriggerPush, Tags: inc("v*")}},
			want: []string{"push"}, lost: map[string]caps.Feature{trigPath + "0": caps.TriggerFilterTags}},
		{name: "pr tags cannot be stated", triggers: []ir.Trigger{{Kind: ir.TriggerPR, Tags: inc("v*")}},
			want: []string{"pull_request"}, lost: map[string]caps.Feature{trigPath + "0": caps.TriggerFilterTags}},
		{name: "tag branches cannot be stated", triggers: []ir.Trigger{{Kind: ir.TriggerPush, Branches: inc("main")}, {Kind: ir.TriggerTag, Tags: inc("v*"), Branches: inc("dev")}},
			want: []string{"- main"}, lost: map[string]caps.Feature{trigPath + "1": caps.TriggerFilterBranch}},
		{name: "workflow_run without workflows", triggers: []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerPipelineDone}},
			want: []string{"push"}, lost: map[string]caps.Feature{trigPath + "1": caps.TriggerPipelineDone}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, rep := emitWith(t, func(pl *ir.Pipeline) { pl.Triggers = c.triggers })
			for _, w := range c.want {
				if !strings.Contains(data, w) {
					t.Errorf("output lacks %q:\n%s", w, data)
				}
			}
			for path, f := range c.lost {
				if got := entryLevel(rep, f, path); got != caps.Lost {
					t.Errorf("%s at %s: level %v, want lost\n%s", f, path, got, data)
				}
			}
			if strings.Contains(data, "workflow_run") {
				t.Errorf("workflow_run emitted without an upstream workflow:\n%s", data)
			}
		})
	}
}

func TestEmitFoldedExcludeIsReported(t *testing.T) {
	data, rep := emitWith(t, func(pl *ir.Pipeline) {
		pl.Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}, Exclude: []string{"wip"}}}}
	})
	if !strings.Contains(data, "'!wip'") {
		t.Fatalf("exclude not folded:\n%s", data)
	}
	if got := entryLevel(rep, caps.TriggerFilterBranch, trigPath+"0"); got != caps.Emulated {
		t.Fatalf("level %v, want emulated", got)
	}
}

func TestEmitOnIssuesAlone(t *testing.T) {
	const src = "name: i\non: issues\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: x\n"
	fsys := fstest.MapFS{".github/workflows/i.yml": &fstest.MapFile{Data: []byte(src)}}
	p, _, err := gha.New(gha.GHA).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	back, files := emitFS(t, p)
	if strings.Contains(string(files[".github/workflows/i.yml"]), "workflow_dispatch") {
		t.Fatalf("spurious workflow_dispatch:\n%s", files[".github/workflows/i.yml"])
	}
	again, _, err := gha.New(gha.GHA).Parse(back)
	if err != nil {
		t.Fatal(err)
	}
	if ok, diff := ir.Equal(p, again); !ok {
		t.Fatal(diff)
	}
}

func TestEmitCallJobFoldsStepFields(t *testing.T) {
	p := baseProject(t)
	j := job(p, "build")
	d := ir.Duration(5 * time.Minute)
	j.Steps = []*ir.Step{{
		ID: "build-step-1", Kind: ir.StepPipelineCall, Action: &ir.ActionRef{Kind: ir.ActionLocal, Path: "./.github/workflows/r.yml"},
		If:  expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{"event"}}, R: lit("push")},
		Env: []ir.Var{{Name: "A", Value: ir.Value{Lit: "1"}}}, Timeout: &d, ContinueOnError: true, WorkDir: "x",
	}}
	j.If = expr.Ref{Path: []string{"ref_name"}}
	files, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	data := string(files[".github/workflows/ci.yml"])
	if !strings.Contains(data, "github.event_name == 'push'") || !strings.Contains(data, "github.ref_name") {
		t.Errorf("step if not folded into the job if:\n%s", data)
	}
	for _, bad := range []string{"timeout-minutes", "env:", "continue-on-error", "working-directory"} {
		if strings.Contains(data, bad) {
			t.Errorf("uses job carries %q:\n%s", bad, data)
		}
	}
	sp := jobBuild + "/step/build-step-1"
	for _, f := range []caps.Feature{caps.StepEnv, caps.StepTimeout, caps.StepContinueOnError, caps.StepWorkdir} {
		if got := entryLevel(rep, f, sp); got != caps.Lost {
			t.Errorf("%s: level %v, want lost", f, got)
		}
	}
	if err := validate.File("gha", "ci.yml", files[".github/workflows/ci.yml"]); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
}

func TestEmitCallJobStepIfFailsSafe(t *testing.T) {
	p := baseProject(t)
	j := job(p, "build")
	j.Steps = []*ir.Step{{
		ID: "build-step-1", Kind: ir.StepPipelineCall, Action: &ir.ActionRef{Kind: ir.ActionLocal, Path: "./.github/workflows/r.yml"},
		If: expr.Opaque{Dialect: "gitlab", Text: "$X"},
	}}
	files, rep, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	data := string(files[".github/workflows/ci.yml"])
	if !strings.Contains(data, "if: false") || entryLevel(rep, caps.StepIf, jobBuild+"/step/build-step-1") != caps.Lost {
		t.Fatalf("call step if must disable the job:\n%s", data)
	}
}

func TestDroneSourceWithoutTriggerStartsOnItsEvents(t *testing.T) {
	pl := &ir.Pipeline{ID: "ci", Jobs: []*ir.Job{{ID: "build", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"make"}}}}}}
	p := &ir.Project{Origin: ir.Origin{Provider: "drone", Dialect: "drone"}, Pipelines: []*ir.Pipeline{pl}}
	_, files := emitFS(t, p)
	var doc string
	for _, f := range files {
		doc = string(f)
	}
	for _, ev := range []string{"push:", "pull_request:", "workflow_dispatch:"} {
		if !strings.Contains(doc, ev) {
			t.Errorf("event %s missing:\n%s", ev, doc)
		}
	}
}
