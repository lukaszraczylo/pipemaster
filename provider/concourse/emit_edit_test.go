package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

// editedParse emits p, replaces old with new in the file and parses the result.
func editedParse(t *testing.T, p *ir.Project, old, replacement string) *ir.Project {
	t.Helper()
	files, _ := emit(t, p)
	src := files[".concourse/ci.yml"]
	if !strings.Contains(src, old) {
		t.Fatalf("%q is not in the file:\n%s", old, src)
	}
	back, _ := parseFiles(t, map[string]string{"ci.yml": strings.Replace(src, old, replacement, 1)})
	return back
}

func TestWorkdirEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].WorkDir = "app"
	back := editedParse(t, project(j), "dir: workspace/app", "dir: workspace/other")
	if got := back.Pipelines[0].Jobs[0].Steps[0].WorkDir; got != "workspace/other" {
		t.Fatalf("workdir = %q", got)
	}
}

func TestGateEditIsKept(t *testing.T) {
	j := scriptJob("deploy", "make deploy")
	j.Manual = true
	j.Environment = &ir.Environment{Name: "prod", Approval: true}
	p := project(j)
	back := editedParse(t, p, "- get: source", "- get: source\n        trigger: true")
	if got := back.Pipelines[0].Jobs[0]; got.Manual || got.Environment != nil {
		t.Fatalf("a triggered job kept the gate: %+v", got)
	}
}

func TestAllowFailureEditIsKept(t *testing.T) {
	j := scriptJob("lint", "make lint")
	j.AllowFailure = true
	back := editedParse(t, project(j), "try:", "do:")
	if back.Pipelines[0].Jobs[0].AllowFailure {
		t.Fatal("a plan that is no try step kept allow_failure")
	}
}

func TestJobConditionEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.If = eqRef("branch", "main")
	back := editedParse(t, project(j), "then exit 0; fi # pipemaster:guard", "then exit 3; fi # pipemaster:guard")
	if back.Pipelines[0].Jobs[0].If != nil {
		t.Fatal("an edited guard moved back to the job")
	}
}

func TestDisabledStepEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = expr2()
	back := editedParse(t, project(j), "guard the step condition has no shell form\n                "+"make", "guard the step condition has no shell form\n                "+"make all")
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.If != nil {
		t.Fatalf("an edited disabled step was restored: %+v", st)
	}
}

func TestSecretMarkerEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Env = []ir.Var{{Name: "REGION", Value: ir.Value{Kind: ir.ValueSecret, Secret: "region"}}}
	back := editedParse(t, project(j), "REGION: ((region))", "REGION: literal")
	env := back.Pipelines[0].Jobs[0].Steps[0].Env
	if len(env) != 1 || env[0].Value.Kind == ir.ValueSecret {
		t.Fatalf("env = %+v", env)
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
