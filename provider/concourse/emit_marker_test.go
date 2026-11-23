package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func scheduleProject() *ir.Project {
	p := project(scriptJob("nightly", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule, Cron: "0 3 * * *"}}
	return p
}

func TestScheduleIsATimeResourceWithoutInitialVersion(t *testing.T) {
	files, rep := emit(t, scheduleProject())
	d := pipelineDoc(t, files)
	var tm map[string]any
	for _, r := range list(d["resources"]) {
		if dict(r)["type"] == "time" {
			tm = dict(r)
		}
	}
	if tm == nil {
		t.Fatalf("no time resource: %v", d["resources"])
	}
	if _, has := dict(tm["source"])["initial_version"]; has {
		t.Fatalf("initial_version must not be set: %v", tm)
	}
	if !hasFeatureEntry(rep, string(caps.TriggerSchedule), "fly check-resource") {
		t.Fatalf("no first-version entry: %v", rep.Entries)
	}
	back, _ := reparse(t, files)
	var got string
	for _, tr := range back.Pipelines[0].Triggers {
		if tr.Kind == ir.TriggerSchedule {
			got = tr.Cron
		}
	}
	if got != "0 3 * * *" {
		t.Fatalf("cron came back as %q", got)
	}
}

func TestScheduleMarkerEditIsIgnored(t *testing.T) {
	files, _ := emit(t, scheduleProject())
	src := files[".concourse/ci.yml"]
	edited := strings.Replace(src, `stop: "03:05"`, `stop: "03:10"`, 1)
	if edited == src {
		t.Fatalf("time source has another shape:\n%s", src)
	}
	back, rep := parseFiles(t, map[string]string{"ci.yml": edited})
	for _, tr := range back.Pipelines[0].Triggers {
		if tr.Kind == ir.TriggerSchedule {
			t.Fatalf("edited schedule was restored: %+v", tr)
		}
	}
	if !hasFeatureEntry(rep, string(caps.TriggerSchedule), "marker ignored") {
		t.Fatalf("no marker-ignored entry: %v", rep.Entries)
	}
}

func matrixJob() *ir.Job {
	j := scriptJob("test", "go test ${{ matrix.go }}")
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "go", Values: []string{"1.21", "1.22"}}}}
	return j
}

func TestMatrixIsAcrossAndParsesBack(t *testing.T) {
	files, _ := emit(t, project(matrixJob()))
	src := files[".concourse/ci.yml"]
	if !strings.Contains(src, "across:") || !strings.Contains(src, "((.:go))") {
		t.Fatalf("no across:\n%s", src)
	}
	back, _ := reparse(t, files)
	j := back.Pipelines[0].Jobs[0]
	if j.Matrix == nil || len(j.Matrix.Axes) != 1 || j.Matrix.Axes[0].Name != "go" {
		t.Fatalf("matrix came back as %+v", j.Matrix)
	}
}

func TestMatrixEditIsKept(t *testing.T) {
	files, _ := emit(t, project(matrixJob()))
	src := files[".concourse/ci.yml"]
	edited := strings.Replace(src, `"1.22"`, `"1.23"`, 1)
	if edited == src {
		edited = strings.Replace(src, "1.22", "1.23", 1)
	}
	back, rep := parseFiles(t, map[string]string{"ci.yml": edited})
	if back.Pipelines[0].Jobs[0].Matrix != nil {
		t.Fatalf("an edited across was restored as the matrix")
	}
	wantIgnored(t, rep, caps.JobMatrix, "across")
}

func actionJob() *ir.Job {
	j := scriptJob("build", "make")
	setup := &ir.Step{ID: "setup", Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "acme", Repo: "thing", Ref: "v1"}}
	j.Steps = append([]*ir.Step{j.Steps[0]}, setup)
	return j
}

func TestHeldActionIsAPlaceholderAndParsesBack(t *testing.T) {
	files, rep := emit(t, project(actionJob()))
	src := files[".concourse/ci.yml"]
	if !strings.Contains(src, "task: setup") || !strings.Contains(src, "# pipemaster:") {
		t.Fatalf("no placeholder task:\n%s", src)
	}
	if !hasFeatureEntry(rep, string(caps.StepAction), "acme/thing") {
		t.Fatalf("no action entry: %v", rep.Entries)
	}
	back, _ := reparse(t, files)
	var st *ir.Step
	for _, s := range back.Pipelines[0].Jobs[0].Steps {
		if s.ID == "setup" {
			st = s
		}
	}
	if st == nil || st.Kind != ir.StepAction || st.Action == nil || st.Action.Repo != "thing" {
		t.Fatalf("action came back as %+v", st)
	}
}

func TestHeldActionEditIsKept(t *testing.T) {
	files, _ := emit(t, project(actionJob()))
	src := files[".concourse/ci.yml"]
	edited := strings.Replace(src, `- "true"`, "- echo edited", 1)
	if edited == src {
		t.Fatalf("placeholder script changed shape:\n%s", src)
	}
	back, rep := parseFiles(t, map[string]string{"ci.yml": edited})
	for _, s := range back.Pipelines[0].Jobs[0].Steps {
		if s.Kind == ir.StepAction {
			t.Fatalf("an edited placeholder was restored as an action: %+v", s)
		}
	}
	wantIgnored(t, rep, caps.StepAction, "setup")
}

func TestNameCollisionIsResolvedDeterministically(t *testing.T) {
	a := scriptJob("a b", "true")
	b := scriptJob("a_b", "true")
	p := project(a, b)
	files, rep := emit(t, p)
	d := pipelineDoc(t, files)
	seen := map[string]bool{}
	for _, j := range list(d["jobs"]) {
		n, _ := dict(j)["name"].(string)
		if seen[n] {
			t.Fatalf("duplicate job name %q", n)
		}
		seen[n] = true
	}
	if len(seen) != 2 || !hasFeatureEntry(rep, string(caps.ExtPassthrough), "collides") {
		t.Fatalf("names %v, entries %v", seen, rep.Entries)
	}
	again, _ := emit(t, project(scriptJob("a b", "true"), scriptJob("a_b", "true")))
	if again[".concourse/ci.yml"] != files[".concourse/ci.yml"] {
		t.Fatal("emission is not deterministic")
	}
}

func TestDisplayNameAndIDComeBack(t *testing.T) {
	j := scriptJob("build", "make")
	j.Name = "Build it"
	files, _ := emit(t, project(j))
	back, _ := reparse(t, files)
	if got := back.Pipelines[0].Jobs[0]; got.ID != "build" || got.Name != "Build it" {
		t.Fatalf("job came back as %q %q", got.ID, got.Name)
	}
}

func TestSecretKindMarker(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Env = []ir.Var{{Name: "REGION", Value: ir.Value{Kind: ir.ValueSecret, Secret: "region"}}}
	files, _ := emit(t, project(j))
	back, _ := reparse(t, files)
	env := back.Pipelines[0].Jobs[0].Steps[0].Env
	if len(env) != 1 || env[0].Value.Kind != ir.ValueSecret || env[0].Value.Secret != "region" {
		t.Fatalf("secret came back as %+v", env)
	}
}

func TestManualGateComesBack(t *testing.T) {
	j := scriptJob("deploy", "make deploy")
	j.Manual = true
	j.Environment = &ir.Environment{Name: "prod", Approval: true}
	files, _ := emit(t, project(j))
	back, _ := reparse(t, files)
	got := back.Pipelines[0].Jobs[0]
	if !got.Manual || got.Environment == nil || got.Environment.Name != "prod" {
		t.Fatalf("gate came back as %+v", got)
	}
}

func TestAllowFailureComesBack(t *testing.T) {
	j := scriptJob("lint", "make lint")
	j.AllowFailure = true
	files, _ := emit(t, project(j))
	if !strings.Contains(files[".concourse/ci.yml"], "try:") {
		t.Fatalf("no try:\n%s", files[".concourse/ci.yml"])
	}
	back, _ := reparse(t, files)
	if !back.Pipelines[0].Jobs[0].AllowFailure {
		t.Fatal("allow_failure was lost on parse")
	}
}

func TestJobConditionMovesBackToTheJob(t *testing.T) {
	j := scriptJob("build", "make")
	j.If = eqRef("branch", "main")
	files, _ := emit(t, project(j))
	back, _ := reparse(t, files)
	got := back.Pipelines[0].Jobs[0]
	if got.If == nil || got.Steps[0].If != nil {
		t.Fatalf("condition is on the job=%v step=%v", got.If, got.Steps[0].If)
	}
}

func TestStepWorkdirComesBack(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].WorkDir = "app"
	files, _ := emit(t, project(j))
	back, _ := reparse(t, files)
	if got := back.Pipelines[0].Jobs[0].Steps[0].WorkDir; got != "app" {
		t.Fatalf("workdir came back as %q", got)
	}
}

func TestEditedStepNameKeepsTheFileAndReportsTheLostName(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Name = "Compile"
	_, rep := editedParse(t, project(j), "task: build-run", "task: renamed")
	wantIgnored(t, rep, caps.DisplayName, "display name", "ID")
}

func TestEditedJobNameKeepsTheFileAndReportsTheLostName(t *testing.T) {
	j := scriptJob("build", "make")
	j.Name = "Build it"
	back, rep := editedParse(t, project(j), "  - name: build\n", "  - name: renamed\n")
	if got := back.Pipelines[0].Jobs[0]; got.ID != "renamed" || got.Name != "" {
		t.Fatalf("job came back as %q %q", got.ID, got.Name)
	}
	wantIgnored(t, rep, caps.DisplayName, "display name", "ID")
}

func TestRenamedPipelineFileKeepsTheNameAndReportsTheLostName(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Name = "CI"
	files, _ := emit(t, p)
	_, rep := parseFiles(t, map[string]string{"other.yml": files[".concourse/ci.yml"]})
	wantIgnored(t, rep, caps.DisplayName, "display name", "ID")
}
