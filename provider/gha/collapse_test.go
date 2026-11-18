package gha_test

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/report"
)

// viaGHA emits p as GHA and parses the files back.
func viaGHA(t *testing.T, p *ir.Project) (*ir.Project, string) {
	t.Helper()
	files, _, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	var text strings.Builder
	for n, b := range files {
		fsys[n] = &fstest.MapFile{Data: b}
		text.Write(b)
	}
	back, _, err := gha.New(gha.GHA).Parse(fsys)
	if err != nil {
		t.Fatalf("%v\n%s", err, text.String())
	}
	return back, text.String()
}

func TestCollapseDefaultsAddedForGHA(t *testing.T) {
	p := baseProject(t)
	pl := p.Pipelines[0]
	pl.Triggers = []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}
	job(p, "build").Runner = ir.Runner{}
	back, text := viaGHA(t, p)
	if got := back.Pipelines[0].Jobs[0].Runner.Labels; len(got) != 0 {
		t.Errorf("default runner label was not collapsed: %v\n%s", got, text)
	}
	for _, tr := range back.Pipelines[0].Triggers {
		if len(tr.Branches.Include)+len(tr.Tags.Include) != 0 {
			t.Errorf("match-all filter was not collapsed: %+v\n%s", tr, text)
		}
	}
}

func TestCollapseRetryLoop(t *testing.T) {
	p := baseProject(t)
	j := job(p, "build")
	j.Retry = &ir.Retry{Attempts: 3, On: []string{"runner_system_failure"}}
	want := slices.Clone(j.Steps[0].Script)
	back, text := viaGHA(t, p)
	bj := back.Pipelines[0].Jobs[0]
	if bj.Retry == nil || bj.Retry.Attempts != 3 || !slices.Equal(bj.Retry.On, j.Retry.On) {
		t.Fatalf("retry = %+v\n%s", bj.Retry, text)
	}
	if !slices.Equal(bj.Steps[0].Script, want) {
		t.Errorf("script = %q, want %q", bj.Steps[0].Script, want)
	}
}

func TestCollapseCacheAndArtifacts(t *testing.T) {
	p := baseProject(t)
	j := job(p, "build")
	j.Cache = []ir.Cache{{Key: "k", Paths: []string{"~/.cache"}}}
	j.Artifacts = []ir.Artifact{{Paths: []string{"bin/"}, Expire: "1 week"}}
	steps := len(j.Steps)
	back, text := viaGHA(t, p)
	bj := back.Pipelines[0].Jobs[0]
	if len(bj.Steps) != steps || len(bj.Cache) != 1 || bj.Cache[0].Policy != "" || bj.Artifacts[0].Expire != "1 week" {
		t.Errorf("cache/artifacts not restored: steps=%d cache=%+v artifacts=%+v\n%s", len(bj.Steps), bj.Cache, bj.Artifacts, text)
	}
}

func TestStatusConditionSurvivesForeignOrigin(t *testing.T) {
	p := baseProject(t)
	p.Origin = ir.Origin{Provider: "gitlab", Dialect: "gitlab"}
	job(p, "build").Steps[0].If = expr.Call{Name: "always"}
	_, text := viaGHA(t, p)
	if !strings.Contains(text, "if: always()") || strings.Contains(text, "if: false") {
		t.Errorf("always() was disabled:\n%s", text)
	}
}

func TestScheduleWithoutCronIsLost(t *testing.T) {
	_, rep := emitWith(t, func(pl *ir.Pipeline) { pl.Triggers = []ir.Trigger{{Kind: ir.TriggerSchedule}} })
	found := false
	for _, e := range rep.Entries {
		found = found || e.Feature == caps.TriggerSchedule && e.Level == caps.Lost
	}
	if !found {
		t.Errorf("no lost trigger.schedule entry: %+v", rep.Entries)
	}
}

func gitlabCondProject(t *testing.T) *ir.Project {
	t.Helper()
	p := baseProject(t)
	p.Origin = ir.Origin{Provider: "gitlab", Dialect: "gitlab"}
	job(p, "build").If = expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: lit("main")}
	return p
}

func parseGHAText(t *testing.T, files map[string][]byte) (*ir.Project, *report.Report) {
	t.Helper()
	fsys := fstest.MapFS{}
	for n, b := range files {
		fsys[n] = &fstest.MapFile{Data: b}
	}
	back, rep, err := gha.New(gha.GHA).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return back, rep
}

func TestConditionMarkerRestoresUneditedCondition(t *testing.T) {
	files, _, err := gha.New(gha.GHA).Emit(gitlabCondProject(t))
	if err != nil {
		t.Fatal(err)
	}
	back, _ := parseGHAText(t, files)
	if got := back.Pipelines[0].Jobs[0].If; expr.String(got) != expr.String(gitlabCondProject(t).Pipelines[0].Jobs[0].If) {
		t.Errorf("condition = %s", expr.String(got))
	}
}

func TestEditedConditionBeatsMarker(t *testing.T) {
	files, _, err := gha.New(gha.GHA).Emit(gitlabCondProject(t))
	if err != nil {
		t.Fatal(err)
	}
	const wf = ".github/workflows/ci.yml"
	var out []string
	for _, l := range strings.Split(string(files[wf]), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "if:") {
			l = "    if: github.ref_name == 'other'"
		}
		out = append(out, l)
	}
	files[wf] = []byte(strings.Join(out, "\n"))
	back, rep := parseGHAText(t, files)
	if got := expr.String(back.Pipelines[0].Jobs[0].If); !strings.Contains(got, "other") {
		t.Errorf("the edited condition must stand, got %s", got)
	}
	noted := false
	for _, e := range rep.Entries {
		noted = noted || strings.Contains(e.Detail, "marker ignored")
	}
	if !noted {
		t.Errorf("no report note for the ignored marker: %+v", rep.Entries)
	}
}

func TestMarkerOfAnotherPipelineIsIgnored(t *testing.T) {
	// The file is ci.yml; the marker names pipeline ci2, so it must not touch this pipeline.
	src := "# pipemaster:emulated trigger.push pipeline/ci2/trigger/0\nname: x\non:\n  push:\n    branches: ['**']\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n"
	back, _ := parseGHAText(t, map[string][]byte{".github/workflows/ci.yml": []byte(src)})
	if got := back.Pipelines[0].Triggers[0].Branches.Include; len(got) != 1 || got[0] != "**" {
		t.Errorf("a marker for pipeline ci2 changed pipeline ci: %v", got)
	}
}

func imageProject(t *testing.T) *ir.Project {
	t.Helper()
	p := baseProject(t)
	st := job(p, "build").Steps[0]
	st.Kind, st.Action, st.With = ir.StepScript, nil, nil
	st.Image = &ir.Image{Ref: "alpine:3.20", Entrypoint: []string{"/bin/sh", "-c"}}
	st.Script = []string{"echo one", "echo two"}
	return p
}

func TestImageStepCollapses(t *testing.T) {
	back, text := viaGHA(t, imageProject(t))
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.Action != nil || st.Image == nil || st.Image.Ref != "alpine:3.20" || !slices.Equal(st.Image.Entrypoint, []string{"/bin/sh", "-c"}) ||
		!slices.Equal(st.Script, []string{"echo one", "echo two"}) {
		t.Errorf("image step not restored: %+v\n%s", st, text)
	}
}

func TestEditedImageStepBeatsMarker(t *testing.T) {
	files, _, err := gha.New(gha.GHA).Emit(imageProject(t))
	if err != nil {
		t.Fatal(err)
	}
	const wf = ".github/workflows/ci.yml"
	files[wf] = []byte(strings.ReplaceAll(string(files[wf]), "docker://alpine:3.20", "docker://alpine:3.21"))
	back, rep := parseGHAText(t, files)
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.Action == nil || st.Action.Image != "alpine:3.21" {
		t.Errorf("the edited docker step must stand: %+v", st)
	}
	noted := false
	for _, e := range rep.Entries {
		noted = noted || strings.Contains(e.Detail, "marker ignored")
	}
	if !noted {
		t.Errorf("no report note: %+v", rep.Entries)
	}
}

func TestStatusGuardedConditionRestoresUnedited(t *testing.T) {
	p := gitlabCondProject(t)
	cond := p.Pipelines[0].Jobs[0].If
	p.Pipelines[0].Jobs[0].If = expr.Binary{Op: expr.OpAnd, L: expr.Call{Name: "failure"}, R: cond}
	files, _, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	back, rep := parseGHAText(t, files)
	if got, want := expr.String(back.Pipelines[0].Jobs[0].If), expr.String(p.Pipelines[0].Jobs[0].If); got != want {
		t.Errorf("condition = %s, want %s", got, want)
	}
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "marker ignored") {
			t.Errorf("an unedited condition was reported edited: %+v", e)
		}
	}
}
