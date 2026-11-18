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
