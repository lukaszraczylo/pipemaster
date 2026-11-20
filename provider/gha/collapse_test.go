package gha_test

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
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

// A placeholder job that gained a real step is the author's job now and must stay.
func TestPlaceholderJobWithRealStepIsKept(t *testing.T) {
	src := "# pipemaster:emulated step.script pipeline/ci\non: push\njobs:\n  pipemaster-empty:\n    runs-on: ubuntu-latest\n    steps:\n      - run: \"true\"\n      - run: make build\n"
	proj, _ := parse(t, map[string]string{"ci.yml": src})
	if n := len(proj.Pipelines[0].Jobs); n != 1 {
		t.Errorf("jobs = %d, want the edited placeholder kept", n)
	}
}

func TestPlaceholderJobAloneIsCollapsed(t *testing.T) {
	src := "# pipemaster:emulated step.script pipeline/ci\non: push\njobs:\n  pipemaster-empty:\n    runs-on: ubuntu-latest\n    steps:\n      - run: \"true\"\n"
	proj, _ := parse(t, map[string]string{"ci.yml": src})
	if n := len(proj.Pipelines[0].Jobs); n != 0 {
		t.Errorf("jobs = %d, want 0", n)
	}
}

func finallyProject(t *testing.T) *ir.Project {
	t.Helper()
	p := baseProject(t)
	p.Origin = ir.Origin{Provider: "gitlab", Dialect: "gitlab"}
	p.Pipelines[0].Finally = []*ir.Job{{
		ID: "cleanup", Runner: ir.Runner{Labels: []string{"ubuntu-latest"}},
		Steps: []*ir.Step{{ID: "c1", Kind: ir.StepScript, Script: []string{"make clean"}}},
	}}
	return p
}

func emitFinally(t *testing.T, edit func(string) string) (*ir.Project, *report.Report) {
	t.Helper()
	files, _, err := gha.New(gha.GHA).Emit(finallyProject(t))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for n, b := range files {
		out[n] = []byte(edit(string(b)))
	}
	return parseGHAText(t, out)
}

func finallyNeeds(p *ir.Project) []string {
	var ids []string
	for _, n := range p.Pipelines[0].Finally[0].Needs {
		ids = append(ids, n.JobID)
	}
	return ids
}

func TestFinallyNeedsRestoreWhenUnedited(t *testing.T) {
	back, _ := emitFinally(t, func(s string) string { return s })
	if got := finallyNeeds(back); len(got) != 0 {
		t.Errorf("needs = %v, want the original empty list", got)
	}
}

func TestEditedFinallyNeedsKeepParsedAndNote(t *testing.T) {
	back, rep := emitFinally(t, func(s string) string {
		i := strings.Index(s, "\n  cleanup:")
		return s[:i] + strings.Replace(s[i:], "      - deploy\n", "", 1)
	})
	if got := finallyNeeds(back); len(got) == 0 {
		t.Errorf("needs = %v, want the edited list kept", got)
	}
	found := false
	for _, e := range rep.Entries {
		found = found || strings.Contains(e.Detail, "marker ignored")
	}
	if !found {
		t.Error("no marker ignored note")
	}
}

func TestCollapseHostMarkerFindsExplicitIDNotSynthesisedCheckout(t *testing.T) {
	const wf = `# pipemaster:emulated step.action pipeline/p/job/test/step/test-step-1
# pipemaster:arg {"host":"https://data.forgejo.org"}
# pipemaster:emulated job.artifacts pipeline/p/job/test
# pipemaster:arg {"pre":1,"post":0}
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - id: test-step-1
        uses: actions/download-artifact@v4
      - run: make test
`
	p, _, err := gha.New(gha.GHA).Parse(fstest.MapFS{".github/workflows/p.yml": {Data: []byte(wf)}})
	if err != nil {
		t.Fatal(err)
	}
	steps := p.Pipelines[0].Jobs[0].Steps
	if len(steps) != 2 || steps[0].Action == nil || steps[0].Action.Repo != "download-artifact" {
		t.Fatalf("steps after collapse = %+v", steps)
	}
	if got := steps[0].Action.Host; got != "https://data.forgejo.org" {
		t.Errorf("host = %q, want the marked source host", got)
	}
}

func TestParseRestoredConditionReadsInSourceDialect(t *testing.T) {
	p := baseProject(t)
	p.Origin.Provider = provider.GitLab
	cond, err := expr.ParseCanonical("branch == 'main'")
	if err != nil {
		t.Fatal(err)
	}
	job(p, "build").If = cond
	back, text := viaGHA(t, p)
	if got := expr.String(back.Pipelines[0].Jobs[0].If); got != expr.String(cond) {
		t.Fatalf("condition = %s, want %s\n%s", got, expr.String(cond), text)
	}
	if back.Origin.Provider != provider.GitLab || back.Origin.Dialect != provider.GitLab {
		t.Errorf("origin = %+v, want provider and dialect %q: the restored condition reads in GitLab's dialect", back.Origin, provider.GitLab)
	}
}

// A directory that mixes a file restored from GitLab with a native GHA file reads in one dialect: the native
// file's conditions are GHA, so the restored file must not flip the project to GitLab.
func TestParseMixedDirKeepsNativeConditionsInGHA(t *testing.T) {
	const native = `name: native
on: push
jobs:
  deploy:
    if: github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    steps:
      - run: make deploy
`
	p := baseProject(t)
	p.Origin.Provider = provider.GitLab
	cond, err := expr.ParseCanonical("branch == 'main'")
	if err != nil {
		t.Fatal(err)
	}
	job(p, "build").If = cond
	files, _, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{".github/workflows/native.yml": {Data: []byte(native)}}
	for n, b := range files {
		fsys[n] = &fstest.MapFile{Data: b}
	}
	alone, _, err := gha.New(gha.GHA).Parse(fstest.MapFS{".github/workflows/native.yml": {Data: []byte(native)}})
	if err != nil {
		t.Fatal(err)
	}
	mixed, rep, err := gha.New(gha.GHA).Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if mixed.Origin.Provider != provider.GHA || mixed.Origin.Dialect != provider.GHA {
		t.Errorf("origin = %+v, want %q: native conditions in the directory read in GHA's dialect", mixed.Origin, provider.GHA)
	}
	again, _, err := gha.New(gha.GHA).Emit(mixed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(again[".github/workflows/native.yml"]), "github.ref == 'refs/heads/main'") {
		t.Errorf("native condition changed on re-emit:\n%s", again[".github/workflows/native.yml"])
	}
	var got expr.Expr
	for _, pl := range mixed.Pipelines {
		if pl.ID == "native" {
			got = pl.Jobs[0].If
		}
	}
	if expr.String(got) != expr.String(alone.Pipelines[0].Jobs[0].If) {
		t.Errorf("native condition = %s, want %s", expr.String(got), expr.String(alone.Pipelines[0].Jobs[0].If))
	}
	if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
		return e.Level == caps.Lost && strings.Contains(e.Detail, "source condition")
	}) {
		t.Errorf("no lost entry for the unrestored condition: %+v", rep.Entries)
	}
}

var registerFakeGL sync.Once

// fakeGitLabOrigin registers a second GitLab-class dialect, so the marker can name one that is not gitlab.
func fakeGitLabOrigin() string {
	const id = "fakegl"
	registerFakeGL.Do(func() {
		d := exprgitlab.Lang
		d.ID = id
		expr.RegisterLang(&d)
	})
	return id
}

func TestParseRestoresConditionFromAnyGitLabClassDialect(t *testing.T) {
	id := fakeGitLabOrigin()
	p := baseProject(t)
	p.Origin = ir.Origin{Provider: id, Dialect: id}
	cond, err := expr.ParseCanonical("branch == 'main'")
	if err != nil {
		t.Fatal(err)
	}
	job(p, "build").If = cond
	back, text := viaGHA(t, p)
	if got := expr.String(back.Pipelines[0].Jobs[0].If); got != expr.String(cond) {
		t.Fatalf("condition = %s, want %s\n%s", got, expr.String(cond), text)
	}
	if back.Origin.Provider != id || back.Origin.Dialect != id {
		t.Errorf("origin = %+v, want %q", back.Origin, id)
	}
	if !strings.Contains(text, `"from":"`+id+`"`) {
		t.Errorf("marker does not name the source dialect:\n%s", text)
	}
}

func TestParseRefusesMarkerNamingAGHAClassDialect(t *testing.T) {
	p := gitlabCondProject(t)
	files, _, err := gha.New(gha.GHA).Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	for n, b := range files {
		files[n] = []byte(strings.ReplaceAll(string(b), `{"src"`, `{"from":"gitea","src"`))
	}
	back, rep := parseGHAText(t, files)
	if back.Origin.Provider == "gitea" || back.Origin.Provider == provider.GitLab {
		t.Errorf("origin = %+v, want the marker ignored", back.Origin)
	}
	if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool { return e.Strategy == "marker ignored" }) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}
