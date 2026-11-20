package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

func TestEmitScriptWorkflow(t *testing.T) {
	st := wpStep("build", "go build ./...", "go test ./...")
	st.WorkDir = "src"
	st.Env = []ir.Var{{Name: "A", Value: ir.Value{Lit: "1"}}, {Name: "TOKEN", Value: ir.Value{Kind: ir.ValueSecret, Secret: "tok"}}}
	st.ContinueOnError = true
	files, _ := emitFiles(t, wpProject("woodpecker", wpJob("ci", st)))
	body, ok := files[dirPrefix+"ci.yaml"]
	if !ok {
		t.Fatalf("want .woodpecker/ci.yaml, got %v", keys(files))
	}
	for _, want := range []string{"name: build", "image: alpine:3", "- go build ./...", "directory: src", "failure: ignore", "from_secret: tok"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, "platform:") && !strings.Contains(body, "labels:") {
		t.Errorf("top-level platform key written:\n%s", body)
	}
}

// Review Focus 1: shell text reaches the shell unchanged.
func TestDollarEscaping(t *testing.T) {
	body, _ := one(t, wpProject(ghaOrigin, wpJob("ci", wpStep("s", "echo $HOME ${FOO} $$"))))
	if !strings.Contains(body, "echo $$HOME $${FOO} $$$$") {
		t.Errorf("every $ must double:\n%s", body)
	}
	back := reparse(t, map[string]string{".woodpecker/ci.yaml": body})
	if got := back.Pipelines[0].Jobs[0].Steps[0].Script[0]; got != "echo $HOME ${FOO} $$" {
		t.Errorf("parsed script = %q, want the original shell text", got)
	}
}

func TestDollarParsedFromWoodpecker(t *testing.T) {
	p, _ := parseRaw(t, map[string]string{".woodpecker/ci.yaml": "steps:\n  - name: s\n    image: alpine\n    commands:\n      - echo $${X}\n"})
	if got := p.Pipelines[0].Jobs[0].Steps[0].Script[0]; got != "echo ${X}" {
		t.Fatalf("script = %q, want echo ${X}", got)
	}
	files, _ := emitFiles(t, p)
	if !strings.Contains(files[dirPrefix+"ci.yaml"], "echo $${X}") {
		t.Errorf("round trip lost the escape:\n%s", files[dirPrefix+"ci.yaml"])
	}
}

func TestExpressionSpelling(t *testing.T) {
	st := wpStep("s",
		"echo ${{ matrix.GO }} ${{ branch }}",
		"echo ${{ env.NAME }} ${{ secrets.TOKEN }}",
		"echo ${{ needs.a.outputs.x }}",
	)
	body, rep := one(t, wpProject(ghaOrigin, wpJob("ci", st)))
	for _, want := range []string{"echo ${GO} ${CI_COMMIT_BRANCH}", "echo $${NAME} $${TOKEN}", "from_secret: TOKEN"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if strings.Contains(body, "echo ${{") || strings.Contains(body, "needs.a") && !strings.Contains(body, "# pipemaster:lost") {
		t.Errorf("a raw expression reached the file:\n%s", body)
	}
	if !reportHas(rep, caps.ScriptInterpolation, caps.Lost) {
		t.Errorf("want a lost script.interpolation entry for the needs output: %+v", rep.Entries)
	}
}

// Review Focus 3: sequential steps stay sequential; a Woodpecker DAG survives a return.
func TestSequentialStepsHaveNoDependsOn(t *testing.T) {
	body, _ := one(t, wpProject(ghaOrigin, wpJob("ci", wpStep("a", "1"), wpStep("b", "2"), wpStep("c", "3"))))
	if strings.Contains(body, "depends_on") {
		t.Errorf("depends_on switches Woodpecker to DAG mode and runs the steps at once:\n%s", body)
	}
}

func TestDAGSurvivesRoundTrip(t *testing.T) {
	src := "steps:\n  - name: a\n    image: alpine\n    commands: [x]\n  - name: b\n    image: alpine\n    commands: [y]\n    depends_on: []\n  - name: c\n    image: alpine\n    commands: [z]\n    depends_on: [a, b]\n"
	p, _ := parseFiles(t, map[string]string{".woodpecker.yaml": src})
	files, _ := emitFiles(t, p)
	body := files[dirPrefix+"woodpecker.yaml"]
	if len(files) != 1 {
		t.Fatalf("files = %v", keys(files))
	}
	back := reparse(t, files)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("Parse(Emit(ir)) differs:\n%s\n%s", diff, body)
	}
	if !strings.Contains(body, "depends_on: []") && !strings.Contains(body, "depends_on:\n") {
		t.Errorf("empty depends_on lost:\n%s", body)
	}
}

func TestTriggers(t *testing.T) {
	for _, c := range []struct {
		name string
		want []string
		not  []string
		tr   []ir.Trigger
	}{
		{name: "push and tag stay two events", tr: []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}, want: []string{"event: push", "event: tag"}},
		{name: "push alone excludes tag pushes", tr: []ir.Trigger{{Kind: ir.TriggerPush, Branches: inc("main")}}, want: []string{"event: push", "branch: main"}, not: []string{"tag"}},
		{name: "pr with paths", tr: []ir.Trigger{{Kind: ir.TriggerPR, Paths: inc("src/*")}}, want: []string{"event: pull_request", "path: src/*"}},
		{name: "manual", tr: []ir.Trigger{{Kind: ir.TriggerManual}}, want: []string{"event: manual"}},
		{name: "schedule", tr: []ir.Trigger{{Kind: ir.TriggerSchedule, Cron: "0 3 * * 1"}}, want: []string{"event: cron"}},
		{name: "exclude branch", tr: []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}, Exclude: []string{"wip"}}}}, want: []string{"exclude:\n        - wip"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
			p.Pipelines[0].Triggers = c.tr
			body, _ := one(t, p)
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q in\n%s", w, body)
				}
			}
			for _, w := range c.not {
				if strings.Contains(body, w) {
					t.Errorf("unexpected %q in\n%s", w, body)
				}
			}
			back := reparse(t, map[string]string{".woodpecker/ci.yaml": body})
			if c.name == "schedule" {
				return
			}
			if got := back.Pipelines[0].Triggers; len(got) != len(c.tr) {
				t.Errorf("triggers after return = %+v, want %+v", got, c.tr)
			}
		})
	}
}

// When items are OR: push on main and every tag must not become an AND.
func TestPushAndTagAreOrItems(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Branches: inc("main")}, {Kind: ir.TriggerTag}}
	body, _ := one(t, p)
	if strings.Count(body, "- event:") != 2 {
		t.Errorf("want two when items:\n%s", body)
	}
	back := reparse(t, map[string]string{".woodpecker/ci.yaml": body})
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("round trip differs:\n%s", diff)
	}
}

func TestInexactGlobIsEmulated(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Branches: inc("release/**", "main")}}
	body, rep := one(t, p)
	if !strings.Contains(body, "release/**") {
		t.Errorf("glob dropped:\n%s", body)
	}
	if !reportHas(rep, caps.TriggerFilterBranch, caps.Emulated) {
		t.Errorf("an inexact glob must be reported as emulated: %+v", rep.Entries)
	}
	if strings.Contains(body, "startsWith") {
		t.Errorf("a glob became a prefix test:\n%s", body)
	}
}

func TestNegatedPatternBecomesExclude(t *testing.T) {
	p := wpProject(ghaOrigin, wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Branches: inc("**", "!wip")}}
	body, rep := one(t, p)
	if !strings.Contains(body, "- wip") || !reportHas(rep, caps.TriggerFilterBranch, caps.Emulated) {
		t.Errorf("want an exclude and an emulated entry:\n%s\n%+v", body, rep.Entries)
	}
}

func TestTagFilterIsRefFilter(t *testing.T) {
	p := wpProject(ghaOrigin, wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerTag, Tags: inc("v*")}}
	body, rep := one(t, p)
	if !strings.Contains(body, "ref: refs/tags/v*") || !reportHas(rep, caps.TriggerFilterTags, caps.Emulated) || !strings.Contains(body, "pipemaster:emulated trigger.filter.tags") {
		t.Errorf("want a ref filter, a marker and an emulated entry:\n%s\n%+v", body, rep.Entries)
	}
}

func TestTriggerFiltersWoodpeckerIgnores(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerTag, Branches: inc("main"), Paths: inc("a")}}
	_, rep := one(t, p)
	if !reportHas(rep, caps.TriggerFilterBranch, caps.Lost) || !reportHas(rep, caps.TriggerFilterPaths, caps.Lost) {
		t.Errorf("branch and path filters of a tag trigger are lost: %+v", rep.Entries)
	}
}

func TestUnrepresentableTriggerNeverRunsUnguarded(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerAPI}}
	body, rep := one(t, p)
	if !strings.Contains(body, `evaluate: "false"`) || !reportHas(rep, caps.TriggerAPI, caps.Lost) {
		t.Errorf("want the fail-safe and a lost entry:\n%s", body)
	}
}

func TestScheduleIsExternal(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule, Cron: "0 3 * * 1"}}
	_, rep := one(t, p)
	if !reportHas(rep, caps.TriggerSchedule, caps.External) || !reportDetail(rep, caps.TriggerSchedule, "0 3 * * 1") || !reportDetail(rep, caps.TriggerSchedule, "Settings > Cron") {
		t.Errorf("want an external entry that names the cron and the setting: %+v", rep.Entries)
	}
}

func TestEnvironmentAndLabels(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"linux/arm64"}}
	body, _ := one(t, wpProject("woodpecker", j))
	if !strings.Contains(body, "labels:\n  platform: linux/arm64") || strings.Contains(body, "\nplatform:") {
		t.Errorf("platform must sit under labels:\n%s", body)
	}
	j = wpJob("ci", wpStep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"ubuntu-latest"}}
	body, rep := one(t, wpProject(ghaOrigin, j))
	if strings.Contains(body, "ubuntu-latest") || !reportHas(rep, caps.JobRunner, caps.Lost) {
		t.Errorf("a hosted runner label is lost, never a platform:\n%s", body)
	}
}

func TestJobAndPipelineEnvCopiedToSteps(t *testing.T) {
	j := wpJob("ci", wpStep("a", "x"), wpStep("b", "y"))
	j.Steps[1].Env = []ir.Var{{Name: "J", Value: ir.Value{Lit: "step"}}}
	j.Env = []ir.Var{{Name: "J", Value: ir.Value{Lit: "job"}}, {Name: "K", Value: ir.Value{Lit: "k"}}}
	p := wpProject(ghaOrigin, j)
	p.Pipelines[0].Env = []ir.Var{{Name: "P", Value: ir.Value{Lit: "p"}}}
	body, rep := one(t, p)
	if strings.Count(body, "P: p") != 2 || strings.Count(body, "K: k") != 2 || !strings.Contains(body, "J: step") {
		t.Errorf("env must reach every step, a step value wins:\n%s", body)
	}
	for _, f := range []caps.Feature{caps.JobEnv, caps.PipelineEnv} {
		if !reportHas(rep, f, caps.Emulated) || !strings.Contains(body, "pipemaster:emulated "+string(f)) {
			t.Errorf("%s needs an emulated entry and a marker:\n%s", f, body)
		}
	}
}

func TestNeedsAndFinally(t *testing.T) {
	a, b := wpJob("a", wpStep("s", "x")), wpJob("b", wpStep("s", "y"))
	b.Needs = []ir.Need{{JobID: "a"}, {JobID: "c", Optional: true}}
	p := wpProject("woodpecker", a, b, wpJob("c", wpStep("s", "z")))
	p.Pipelines[0].Finally = []*ir.Job{wpJob("done", wpStep("s", "w"))}
	files, rep := emitFiles(t, p)
	if !strings.Contains(files[dirPrefix+"b.yaml"], "- a\n") || !strings.Contains(files[dirPrefix+"b.yaml"], "name: c\n    optional: true") {
		t.Errorf("depends_on:\n%s", files[dirPrefix+"b.yaml"])
	}
	fin := files[dirPrefix+"done.yaml"]
	for _, w := range []string{"- success", "- failure", "name: a", "name: b", "name: c", "optional: true"} {
		if !strings.Contains(fin, w) {
			t.Errorf("finally workflow lacks %q:\n%s", w, fin)
		}
	}
	if !reportHas(rep, caps.PipelineFinally, caps.Emulated) || !strings.Contains(fin, "pipemaster:emulated pipeline.finally") {
		t.Errorf("finally needs an entry and a marker:\n%s", fin)
	}
}

func TestEmitMatrix(t *testing.T) {
	t.Run("axes are native", func(t *testing.T) {
		j := wpJob("ci", wpStep("s", "go test ${{ matrix.GO }}"))
		j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "GO", Values: []string{"1.22", "1.23"}}}}
		body, rep := one(t, wpProject("woodpecker", j))
		if !strings.Contains(body, `- "1.22"`) || !strings.Contains(body, "go test ${GO}") || reportHas(rep, caps.JobMatrix, caps.Emulated) {
			t.Errorf("matrix:\n%s", body)
		}
		back := reparse(t, map[string]string{".woodpecker/ci.yaml": body})
		if ok, diff := ir.Equal(wpProject("woodpecker", j), back); !ok {
			t.Errorf("round trip differs:\n%s", diff)
		}
	})
	t.Run("exclude is expanded", func(t *testing.T) {
		j := wpJob("ci", wpStep("s", "x"))
		j.Matrix = &ir.Matrix{
			Axes:    []ir.Axis{{Name: "OS", Values: []string{"a", "b"}}, {Name: "V", Values: []string{"1", "2"}}},
			Exclude: []ir.Combo{{{Key: "OS", Value: "a"}, {Key: "V", Value: "2"}}},
		}
		body, rep := one(t, wpProject(ghaOrigin, j))
		if strings.Count(body, "OS:") != 3 || !reportHas(rep, caps.JobMatrix, caps.Emulated) {
			t.Errorf("want 3 explicit entries and an emulated entry:\n%s", body)
		}
	})
	t.Run("fail-fast is lost", func(t *testing.T) {
		ff := false
		j := wpJob("ci", wpStep("s", "x"))
		j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "A", Values: []string{"1"}}}, FailFast: &ff, MaxParallel: 2}
		_, rep := one(t, wpProject(ghaOrigin, j))
		if !reportHas(rep, caps.JobMatrix, caps.Lost) {
			t.Errorf("fail-fast and max-parallel have no form: %+v", rep.Entries)
		}
	})
}

func TestEmitServices(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:16"}, Env: []ir.Var{{Name: "POSTGRES_PASSWORD", Value: ir.Value{Lit: "pw"}}}, Ports: []string{"5432"}}}
	body, _ := one(t, wpProject("woodpecker", j))
	if !strings.Contains(body, "services:\n  db:\n    image: postgres:16") {
		t.Errorf("services:\n%s", body)
	}
	back := reparse(t, map[string]string{".woodpecker/ci.yaml": body})
	if ok, diff := ir.Equal(wpProject("woodpecker", j), back); !ok {
		t.Errorf("round trip differs:\n%s", diff)
	}
}

func TestJobContainerAndDefaultImage(t *testing.T) {
	st := &ir.Step{ID: "s", Kind: ir.StepScript, Script: []string{"x"}}
	j := wpJob("ci", st)
	j.Container = &ir.Image{Ref: "golang:1.23"}
	body, _ := one(t, wpProject(ghaOrigin, j))
	if !strings.Contains(body, "image: golang:1.23") {
		t.Errorf("the job container is the step image:\n%s", body)
	}
	j = wpJob("ci", &ir.Step{ID: "s", Kind: ir.StepScript, Script: []string{"x"}})
	body, rep := one(t, wpProject(ghaOrigin, j))
	if !strings.Contains(body, "image: ubuntu:latest") || !reportHas(rep, caps.JobContainer, caps.Emulated) {
		t.Errorf("a missing image needs the reported default:\n%s\n%+v", body, rep.Entries)
	}
}

func TestEmitPluginStep(t *testing.T) {
	st := &ir.Step{ID: "push", Kind: ir.StepPlugin, Image: &ir.Image{Ref: "plugins/slack"}, With: map[string]ir.Value{
		"repo": {Lit: "a/b"}, "dry_run": {Lit: "true"}, "password": {Kind: ir.ValueSecret, Secret: "pw"},
	}}
	body, _ := one(t, wpProject("woodpecker", wpJob("ci", st)))
	if !strings.Contains(body, "dry_run: true") || strings.Contains(body, `"true"`) || !strings.Contains(body, "from_secret: pw") {
		t.Errorf("plugin settings:\n%s", body)
	}
	back := reparse(t, map[string]string{".woodpecker/ci.yaml": body})
	if ok, diff := ir.Equal(wpProject("woodpecker", wpJob("ci", st)), back); !ok {
		t.Errorf("round trip differs:\n%s", diff)
	}
}

func TestDerivedIDIsNotWrittenAsName(t *testing.T) {
	a, b := wpStep("step-0", "x"), wpStep("named", "y")
	a.Ext = ir.Ext{"woodpecker": map[string]any{woodpecker.ExtDerivedID: true}}
	body, _ := one(t, wpProject("woodpecker", wpJob("ci", a, b)))
	if strings.Contains(body, "name: step-0") || !strings.Contains(body, "name: named") {
		t.Errorf("a derived id must not become a name:\n%s", body)
	}
	ga := wpStep("ci-step-1", "x")
	ga.Ext = ir.Ext{"gha": map[string]any{"_derived_id": true}}
	body, _ = one(t, wpProject(ghaOrigin, wpJob("ci", ga)))
	if strings.Contains(body, "ci-step-1") {
		t.Errorf("a GHA derived id leaked:\n%s", body)
	}
}

func TestMapKeyIsRestored(t *testing.T) {
	src := "steps:\n  build:\n    name: compile\n    image: alpine\n    commands: [x]\n  test:\n    image: alpine\n    commands: [y]\n"
	p, _ := parseFiles(t, map[string]string{".woodpecker.yaml": src})
	files, _ := emitFiles(t, p)
	body := files[dirPrefix+"woodpecker.yaml"]
	if !strings.Contains(body, "steps:\n  build:\n    name: compile") || !strings.Contains(body, "\n  test:\n") {
		t.Errorf("map form lost:\n%s", body)
	}
	if ok, diff := ir.Equal(p, reparse(t, files)); !ok {
		t.Errorf("round trip differs:\n%s", diff)
	}
}

func TestEmitConditions(t *testing.T) {
	t.Run("translatable subset", func(t *testing.T) {
		j := wpJob("ci", wpStep("s", "x"))
		j.If = ghaCond(t, "github.ref_type == 'branch' && startsWith(github.ref_name, 'release')")
		body, _ := one(t, wpProject(ghaOrigin, j))
		if !strings.Contains(body, "evaluate:") || strings.Contains(body, `"false"`) {
			t.Errorf("a translatable condition must stay a condition:\n%s", body)
		}
	})
	t.Run("untranslatable is disabled", func(t *testing.T) {
		j := wpJob("ci", wpStep("s", "x"))
		j.If = expr.Binary{Op: expr.OpEq, L: ref("branch"), R: expr.Lit{Kind: expr.LitStr, Str: "main"}}
		j.Steps[0].If = expr.Opaque{Dialect: ghaOrigin, Text: "contains(x, 'y')"}
		body, rep := one(t, wpProject(ghaOrigin, j))
		if strings.Count(body, `evaluate: "false"`) != 2 || !strings.Contains(body, "# pipemaster:lost job.if") {
			t.Errorf("want the fail-safe on the job and the step plus a lost comment:\n%s", body)
		}
		if !reportHas(rep, caps.JobIf, caps.Lost) || !reportHas(rep, caps.StepIf, caps.Lost) || !reportHas(rep, caps.ExprOpaque, caps.Lost) {
			t.Errorf("entries: %+v", rep.Entries)
		}
	})
	t.Run("status calls become status", func(t *testing.T) {
		j := wpJob("ci", wpStep("s", "x"))
		j.Steps[0].If = expr.Call{Name: "failure"}
		j.If = expr.Call{Name: "always"}
		body, _ := one(t, wpProject(ghaOrigin, j))
		if !strings.Contains(body, "status:\n      - failure") && !strings.Contains(body, "- failure") {
			t.Errorf("status lost:\n%s", body)
		}
	})
	t.Run("job condition ANDs with triggers", func(t *testing.T) {
		j := wpJob("ci", wpStep("s", "x"))
		j.If = ghaCond(t, "startsWith(github.sha, 'def')")
		p := wpProject(ghaOrigin, j)
		p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerManual}}
		body, _ := one(t, p)
		if strings.Count(body, "evaluate:") != 2 {
			t.Errorf("each trigger entry carries the condition:\n%s", body)
		}
	})
}

func TestManualJob(t *testing.T) {
	j := wpJob("deploy", wpStep("s", "x"))
	j.Manual = true
	body, rep := one(t, wpProject(ghaOrigin, j))
	if !strings.Contains(body, "event: manual") || !reportHas(rep, caps.JobManual, caps.Emulated) || !strings.Contains(body, "pipemaster:emulated job.manual") {
		t.Errorf("manual job:\n%s", body)
	}
}

func TestExternalAndLostEntries(t *testing.T) {
	to := ir.Duration(60e9)
	j := wpJob("ci", wpStep("s", "x"))
	j.Timeout = &to
	j.Environment = &ir.Environment{Name: "prod", Approval: true}
	j.Retry = &ir.Retry{Attempts: 2}
	j.Outputs = []ir.Output{{Name: "o", Value: "v"}}
	j.Steps[0].Timeout = &to
	j.Steps[0].Shell = "bash"
	p := wpProject(ghaOrigin, j)
	p.Pipelines[0].Permissions = &ir.Permissions{Scopes: map[string]string{"contents": "read"}}
	p.Pipelines[0].Inputs = []ir.Input{{Name: "i"}}
	_, rep := one(t, p)
	for f, lvl := range map[caps.Feature]caps.Level{
		caps.JobTimeout: caps.External, caps.JobEnvironmentApproval: caps.External, caps.JobEnvironment: caps.Lost,
		caps.JobRetry: caps.Lost, caps.JobOutputs: caps.Lost, caps.StepTimeout: caps.Lost, caps.StepShell: caps.Lost,
		caps.PipelinePermissions: caps.Lost, caps.PipelineInputs: caps.Lost,
	} {
		if !reportHas(rep, f, lvl) {
			t.Errorf("want %s at %v: %+v", f, lvl, rep.Entries)
		}
	}
	if !reportDetail(rep, caps.JobTimeout, "1m0s") {
		t.Errorf("the timeout entry must name the value to set: %+v", rep.Entries)
	}
}

func TestAllowFailureAndCacheAreEmulated(t *testing.T) {
	j := wpJob("ci", wpStep("a", "x"), wpStep("b", "y"))
	j.AllowFailure = true
	j.Cache = []ir.Cache{{Key: "k", Paths: []string{"p"}}}
	j.Artifacts = []ir.Artifact{{Name: "n", Paths: []string{"p"}}}
	body, rep := one(t, wpProject(ghaOrigin, j))
	if strings.Count(body, "failure: ignore") != 2 {
		t.Errorf("every step ignores failure:\n%s", body)
	}
	for _, f := range []caps.Feature{caps.JobAllowFailure, caps.JobCache, caps.JobArtifacts} {
		if !reportHas(rep, f, caps.Emulated) || !strings.Contains(body, "pipemaster:emulated "+string(f)) {
			t.Errorf("%s needs an emulated entry and a marker:\n%s", f, body)
		}
	}
}

func TestConcurrency(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Concurrency = &ir.Concurrency{Group: "deploy-${{ branch }}", CancelInProgress: true}
	body, rep := one(t, p)
	if !strings.Contains(body, "limit: 1") || !strings.Contains(body, "group: deploy-${CI_COMMIT_BRANCH}") {
		t.Errorf("concurrency:\n%s", body)
	}
	if !reportDetail(rep, caps.PipelineConcurrency, "cancel-in-progress") {
		t.Errorf("cancel-in-progress is lost: %+v", rep.Entries)
	}
}

func TestDisplayNames(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Name = "Build it"
	p := wpProject("woodpecker", j)
	p.Pipelines[0].Name = "Main"
	_, rep := one(t, p)
	if !reportHas(rep, caps.DisplayName, caps.Lost) || len(rep.Entries) < 2 {
		t.Errorf("pipeline and job names are lost: %+v", rep.Entries)
	}
}

func TestForeignExtIsReported(t *testing.T) {
	st := wpStep("s", "x")
	st.Ext = ir.Ext{"gha": map[string]any{"uses_extra": "y"}}
	_, rep := one(t, wpProject(ghaOrigin, wpJob("ci", st)))
	if !reportHas(rep, caps.ExtPassthrough, caps.Lost) {
		t.Errorf("other providers' Ext must be reported: %+v", rep.Entries)
	}
}

func TestOwnExtIsWritten(t *testing.T) {
	src := "clone:\n  git:\n    image: woodpeckerci/plugin-git\n    settings:\n      depth: 1\nsteps:\n  - name: a\n    image: alpine\n    commands: [x]\n    detach: true\n    when:\n      - event: push\n        path: 'src/**'\n"
	p, _ := parseFiles(t, map[string]string{".woodpecker.yaml": src})
	files, _ := emitFiles(t, p)
	body := files[dirPrefix+"woodpecker.yaml"]
	for _, w := range []string{"depth: 1", "detach: true", "src/**"} {
		if !strings.Contains(body, w) {
			t.Errorf("Ext key lost, missing %q:\n%s", w, body)
		}
	}
	if ok, diff := ir.Equal(p, reparse(t, files)); !ok {
		t.Errorf("round trip differs:\n%s", diff)
	}
}

func TestMultiplePipelinesBecomePrefixedWorkflows(t *testing.T) {
	p := wpProject("woodpecker", wpJob("build", wpStep("s", "x")))
	p.Pipelines[0].ID = "aaa"
	p.Pipelines = append(p.Pipelines, &ir.Pipeline{ID: "release", Jobs: []*ir.Job{wpJob("build", wpStep("s", "y"))}, Triggers: []ir.Trigger{{Kind: ir.TriggerTag}}})
	files, rep := emitFiles(t, p)
	if len(files) != 2 || files[dirPrefix+"aaa-build.yaml"] == "" {
		t.Fatalf("files = %v", keys(files))
	}
	rel := files[dirPrefix+"release-build.yaml"]
	if !strings.Contains(rel, "event: tag") || !strings.Contains(rel, "pipemaster:emulated project.multi_pipeline pipeline/release") {
		t.Errorf("workflow of the second pipeline:\n%s", rel)
	}
	if !reportHas(rep, caps.ProjectMultiPipeline, caps.Emulated) {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestEmittedIDs(t *testing.T) {
	p := wpProject(ghaOrigin, wpJob("my job", wpStep("s", "x")))
	p.Pipelines[0].ID = "ci"
	m, ok := woodpecker.New().(interface {
		EmittedIDs(*ir.Project) map[string]string
	})
	if !ok {
		t.Fatal("the adapter must report renamed ids")
	}
	got := m.EmittedIDs(p)
	if got["pipeline/woodpecker"] != "ci" || got["pipeline/woodpecker/job/my-job"] != "my job" {
		t.Errorf("EmittedIDs = %v", got)
	}
	files, _ := emitFiles(t, p)
	if _, has := files[dirPrefix+"my-job.yaml"]; !has {
		t.Errorf("files = %v", keys(files))
	}
}

func TestEveryNonNativeCapIsExercised(t *testing.T) {
	tested := map[caps.Feature]bool{
		caps.JobEnv: true, caps.PipelineEnv: true, caps.ProjectMultiPipeline: true, caps.JobCache: true, caps.JobArtifacts: true,
		caps.JobAllowFailure: true, caps.PipelineFinally: true, caps.JobManual: true, caps.TriggerFilterTags: true,
		caps.TriggerSchedule: true, caps.JobTimeout: true, caps.JobEnvironmentApproval: true,
	}
	for f, s := range woodpecker.New().Caps() {
		if (s.Level == caps.Emulated || s.Level == caps.External) && !tested[f] {
			t.Errorf("%s is %v but has no emulation or external test", f, s.Level)
		}
	}
}

func ghaCond(t *testing.T, src string) expr.Expr {
	t.Helper()
	e, err := exprgha.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCommentsEscapeDollar(t *testing.T) {
	j := wpJob("ci", wpStep("s", "echo ${#a[@]} ${{ needs.x.outputs.v }} $HOME"))
	body, _ := one(t, wpProject("gha", j))
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "# pipemaster:") {
			continue
		}
		if strings.Contains(strings.ReplaceAll(line, "$$", ""), "$") {
			t.Errorf("a comment line holds an unescaped $: %s", line)
		}
	}
	if !strings.Contains(body, "# pipemaster:lost") {
		t.Fatalf("the case must produce a lost comment:\n%s", body)
	}
}
