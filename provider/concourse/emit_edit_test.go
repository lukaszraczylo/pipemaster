package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

// editedParse emits p, replaces old with new in the file and parses the result.
func editedParse(t *testing.T, p *ir.Project, old, replacement string) (*ir.Project, *report.Report) {
	t.Helper()
	files, _ := emit(t, p)
	src := files[".concourse/ci.yml"]
	if !strings.Contains(src, old) {
		t.Fatalf("%q is not in the file:\n%s", old, src)
	}
	return parseFiles(t, map[string]string{"ci.yml": strings.Replace(src, old, replacement, 1)})
}

// wantIgnored fails unless the report has a marker-ignored entry of the feature that names every lost field.
func wantIgnored(t *testing.T, rep *report.Report, feature caps.Feature, lost ...string) {
	t.Helper()
	for _, e := range rep.Entries {
		if e.Feature != feature || e.Level != caps.Lost || !strings.Contains(e.Detail, "marker ignored") {
			continue
		}
		ok := true
		for _, f := range lost {
			ok = ok && strings.Contains(e.Detail, f)
		}
		if ok {
			return
		}
	}
	t.Fatalf("no marker-ignored entry of %s naming %v: %v", feature, lost, rep.Entries)
}

func TestWorkdirEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].WorkDir = "app"
	back, rep := editedParse(t, project(j), "dir: workspace/app", "dir: workspace/other")
	if got := back.Pipelines[0].Jobs[0].Steps[0].WorkDir; got != "workspace/other" {
		t.Fatalf("workdir = %q", got)
	}
	wantIgnored(t, rep, caps.StepWorkdir, "run.dir")
}

func TestGateEditIsKept(t *testing.T) {
	j := scriptJob("deploy", "make deploy")
	j.Manual = true
	j.Environment = &ir.Environment{Name: "prod", Approval: true}
	p := project(j)
	back, rep := editedParse(t, p, "- get: source", "- get: source\n        trigger: true")
	if got := back.Pipelines[0].Jobs[0]; got.Manual || got.Environment != nil {
		t.Fatalf("a triggered job kept the gate: %+v", got)
	}
	wantIgnored(t, rep, caps.JobManual, "triggering gets")
}

func TestAllowFailureEditIsKept(t *testing.T) {
	j := scriptJob("lint", "make lint")
	j.AllowFailure = true
	back, rep := editedParse(t, project(j), "try:", "do:")
	if back.Pipelines[0].Jobs[0].AllowFailure {
		t.Fatal("a plan that is no try step kept allow_failure")
	}
	wantIgnored(t, rep, caps.JobAllowFailure, "try")
}

func TestJobConditionEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.If = eqRef("branch", "main")
	back, rep := editedParse(t, project(j), "then exit 0; fi # pipemaster:guard", "then exit 3; fi # pipemaster:guard")
	if back.Pipelines[0].Jobs[0].If != nil {
		t.Fatal("an edited guard moved back to the job")
	}
	wantIgnored(t, rep, caps.JobIf, "guard")
}

func TestDisabledStepEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = expr2()
	back, rep := editedParse(t, project(j), "guard the step condition has no shell form\n                "+"make", "guard the step condition has no shell form\n                "+"make all")
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.If != nil {
		t.Fatalf("an edited disabled step was restored: %+v", st)
	}
	wantIgnored(t, rep, caps.StepIf, "script")
}

func TestVarMarkerEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Env = []ir.Var{{Name: "REGION", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixVars, "region"}}}}}
	back, rep := editedParse(t, project(j), "REGION: ((region))", "REGION: literal")
	env := back.Pipelines[0].Jobs[0].Steps[0].Env
	if len(env) != 1 || env[0].Value.Kind != ir.ValueLit {
		t.Fatalf("env = %+v", env)
	}
	wantIgnored(t, rep, caps.ConfigName, "REGION")
}

func TestVarComesBackAsVarFromItsMarker(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Env = []ir.Var{{Name: "REGION", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.PrefixVars, "region"}}}}}
	files, _ := emit(t, project(j))
	back, rep := reparse(t, files)
	env := back.Pipelines[0].Jobs[0].Steps[0].Env
	if len(env) != 1 || env[0].Value.Kind != ir.ValueExpr || expr.String(env[0].Value.Expr) != "vars.region" {
		t.Fatalf("env = %+v", env)
	}
	if hasFeatureEntry(rep, "config.name", "treated as secrets") {
		t.Fatalf("a file with markers needs no secret note: %v", rep.Entries)
	}
}

// expr2 is a condition with no shell test form.
func expr2() expr.Expr {
	return expr.Call{Name: "contains", Args: []expr.Expr{expr.Ref{Path: []string{"branch"}}, expr.Lit{Kind: expr.LitStr, Str: "x"}}}
}

func TestDisabledStepComesBack(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = expr2()
	files, rep := emit(t, project(j))
	if !hasFeatureEntry(rep, "step.if", "no shell test form") {
		t.Fatalf("no lost entry: %v", rep.Entries)
	}
	back, _ := reparse(t, files)
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.If == nil || len(st.Script) != 1 || st.Script[0] != "make" {
		t.Fatalf("disabled step came back as %+v", st)
	}
}

func TestEditedHeldJobStaysManual(t *testing.T) {
	notify := scriptJob("notify", "echo failed")
	notify.Needs = []ir.Need{{JobID: "build"}}
	notify.If = expr.Call{Name: "failure"}
	p := project(scriptJob("build", "make"), notify)
	back, rep := editedParse(t, p, "echo failed", "echo failed; echo more")
	var got *ir.Job
	for _, j := range back.Pipelines[0].Jobs {
		if j.ID == "notify" {
			got = j
		}
	}
	if got == nil || !got.Manual {
		t.Fatalf("an edited held job runs with no gate: %+v", got)
	}
	if !hasEntry(rep, "stays manual") {
		t.Fatalf("no entry says the job stays manual: %v", rep.Entries)
	}
}
