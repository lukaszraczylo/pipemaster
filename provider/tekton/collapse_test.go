package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const taskFile = ".tekton/tasks/ci-a.yaml"

// editedMarker emits p, edits one file with edit, and checks that the parser keeps the edit and reports
// "marker ignored".
func editedMarker(t *testing.T, p *ir.Project, file, from, to string) *ir.Project {
	t.Helper()
	files, _ := emit(t, p)
	if !strings.Contains(files[file], from) {
		t.Fatalf("%s lacks %q:\n%s", file, from, files[file])
	}
	i := strings.LastIndex(files[file], from)
	files[file] = files[file][:i] + to + files[file][i+len(from):]
	back, rep := reparse(t, files)
	if !hasEntry(rep, "marker ignored") {
		t.Fatalf("no marker ignored entry: %v", rep.Entries)
	}
	return back
}

func TestEditedDefaultImageIsKept(t *testing.T) {
	j := &ir.Job{ID: "a", Steps: []*ir.Step{{ID: "run", Kind: ir.StepScript, Script: []string{"make"}}}}
	back := editedMarker(t, project(j), taskFile, "image: alpine:3.22", "image: busybox")
	if got := back.Pipelines[0].Jobs[0].Steps[0].Image; got == nil || got.Ref != "busybox" {
		t.Fatalf("image = %+v", got)
	}
}

func TestEditedNoopStepIsKept(t *testing.T) {
	j := &ir.Job{ID: "a"}
	back := editedMarker(t, project(j), taskFile, `script: "true"`, "script: echo hi")
	if st := back.Pipelines[0].Jobs[0].Steps; len(st) != 1 || st[0].Script[0] != "echo hi" {
		t.Fatalf("steps = %+v", st)
	}
}

func TestEditedOutputsStepIsKept(t *testing.T) {
	j := scriptJob("a", "true")
	j.Outputs = []ir.Output{{Name: "v", Value: "x"}}
	back := editedMarker(t, project(j), taskFile, `"${PM_OUT_0}"`, `"other"`)
	if o := back.Pipelines[0].Jobs[0].Outputs; len(o) != 1 || o[0].Value != "" {
		t.Fatalf("outputs restored: %+v", back.Pipelines[0].Jobs[0].Outputs)
	}
}

func TestEditedMatrixIsKept(t *testing.T) {
	j := scriptJob("a", "true")
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "os", Values: []string{"l", "m"}}, {Name: "v", Values: []string{"1", "2"}}},
		Exclude: []ir.Combo{{{Key: "os", Value: "m"}, {Key: "v", Value: "2"}}},
	}
	back := editedMarker(t, project(j), ".tekton/pipeline.yaml", "value: m", "value: z")
	if m := back.Pipelines[0].Jobs[0].Matrix; m == nil || len(m.Exclude) != 0 {
		t.Fatalf("matrix restored: %+v", m)
	}
}

func TestEditedFinallyNeedsAreKept(t *testing.T) {
	f := scriptJob("f", "true")
	f.Needs = []ir.Need{{JobID: "main"}}
	p := project(scriptJob("main", "true"), scriptJob("other", "true"))
	p.Pipelines[0].Finally = []*ir.Job{f}
	back := editedMarker(t, p, ".tekton/pipeline.yaml", "  finally:\n    - name: f\n", "  finally:\n    - name: f\n      runAfter:\n        - other\n")
	if n := back.Pipelines[0].Finally[0].Needs; len(n) != 1 || n[0].JobID != "other" {
		t.Fatalf("needs = %+v", n)
	}
}

func TestEditedShellRefIsKept(t *testing.T) {
	back := editedMarker(t, project(scriptJob("a", "echo ${{ vars.REGION }}")), taskFile, "${REGION}", "${REGION} ${REGION}")
	if got := back.Pipelines[0].Jobs[0].Steps[0].Script[0]; strings.Contains(got, "vars.REGION") {
		t.Fatalf("script = %q", got)
	}
}

func TestEditedPipelineEnvIsKept(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Env = []ir.Var{{Name: "MODE", Value: ir.Value{Lit: "fast"}}}
	back := editedMarker(t, p, taskFile, "value: fast", "value: slow")
	if len(back.Pipelines[0].Env) != 0 {
		t.Fatalf("env restored: %+v", back.Pipelines[0].Env)
	}
}

func TestEditedJobGuardIsKept(t *testing.T) {
	j := scriptJob("a", "make")
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"l"}}}}
	j.If = eqStr([]string{"matrix", "os"}, "l")
	files, _ := emit(t, project(j))
	if !strings.Contains(files[taskFile], "pipemaster:guard") {
		t.Fatalf("no guard line in the task:\n%s", files[taskFile])
	}
	back := editedMarker(t, project(j), taskFile, "= 'l'", "= 'z'")
	got := back.Pipelines[0].Jobs[0]
	if got.If != nil {
		t.Fatalf("job condition restored: %v", got.If)
	}
	if c := cond(got.Steps[0].If); !strings.Contains(c, "'z'") {
		t.Fatalf("the edit is lost: step condition %q", c)
	}
}

func TestEditedServicePortsAreKept(t *testing.T) {
	j := scriptJob("a", "true")
	j.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:16"}, Ports: []string{"5432:5432"}}}
	back := editedMarker(t, project(j), taskFile, "containerPort: 5432", "containerPort: 6543")
	if p := back.Pipelines[0].Jobs[0].Services[0].Ports; len(p) != 1 || p[0] != "6543" {
		t.Fatalf("ports = %v", p)
	}
}

func TestEditedActionStepIsKept(t *testing.T) {
	co := actionStep("co", "checkout@v4", map[string]string{"fetch-depth": "1"})
	back := editedMarker(t, project(jobWith(co)), taskFile, "name: depth", "name: edited")
	if st := back.Pipelines[0].Jobs[0].Steps[0]; st.Action != nil {
		t.Fatalf("action restored: %+v", st)
	}
}

func TestEditedWorkDirIsKept(t *testing.T) {
	j := &ir.Job{ID: "a", Steps: []*ir.Step{
		{ID: "co", Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "actions", Repo: "checkout", Ref: "v4"}},
		{ID: "run", Kind: ir.StepScript, Script: []string{"ls"}, WorkDir: "src"},
	}}
	back := editedMarker(t, project(j), taskFile, "workingDir: /workspace/src", "workingDir: /workspace/lib")
	if got := back.Pipelines[0].Jobs[0].Steps[1].WorkDir; got != "/workspace/lib" {
		t.Fatalf("work dir = %q", got)
	}
}

func TestEditedPipelineEnvExpressionIsKept(t *testing.T) {
	p := project(scriptJob("a", "true"))
	p.Pipelines[0].Env = []ir.Var{{Name: "REF", Value: ir.Value{Kind: ir.ValueExpr, Expr: refExpr("branch")}}}
	back := editedMarker(t, p, taskFile, "$(params.ctx-branch)", "$(params.ctx-sha)")
	if len(back.Pipelines[0].Env) != 0 {
		t.Fatalf("env restored: %+v", back.Pipelines[0].Env)
	}
	var kept bool
	for _, st := range back.Pipelines[0].Jobs[0].Env {
		kept = kept || st.Name == "REF"
	}
	if !kept {
		t.Fatalf("the edited env must stay on the job: %+v", back.Pipelines[0].Jobs[0].Env)
	}
}
