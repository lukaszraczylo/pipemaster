package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
)

func emitParse(t *testing.T, p *ir.Project, edit func(string) string) (string, *ir.Project, *report.Report) {
	t.Helper()
	files, _, err := gitlab.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	text := string(files[rootFile])
	if edit != nil {
		text = edit(text)
	}
	back, rep := parseFiles(t, map[string]string{rootFile: text})
	return text, back, rep
}

func ignoredNote(rep *report.Report) bool {
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "marker ignored") {
			return true
		}
	}
	return false
}

// A shell variable that reads like a converted matrix reference must stay a shell variable.
func TestRefsRestoreOnlyAtRecordedPosition(t *testing.T) {
	st := glStep("echo $x", "echo ${{ matrix.x }}")
	_, back, _ := emitParse(t, glProject("gitlab", glJob("build", st)), nil)
	got := back.Pipelines[0].Jobs[0].Steps[0].Script
	if len(got) != 2 || got[0] != "echo $x" || got[1] != "echo ${{ matrix.x }}" {
		t.Errorf("script = %q", got)
	}
}

func TestRefsRestoreEveryFlatteningSite(t *testing.T) {
	const tpl = "${{ matrix.os }}"
	sites := map[string]struct {
		build func(j *ir.Job)
		read  func(j *ir.Job) string
	}{
		"image":       {func(j *ir.Job) { j.Container = &ir.Image{Ref: "alpine:" + tpl} }, func(j *ir.Job) string { return j.Container.Ref }},
		"service":     {func(j *ir.Job) { j.Services = []ir.Service{{Image: ir.Image{Ref: "pg:" + tpl}, Name: "db-" + tpl}} }, func(j *ir.Job) string { return j.Services[0].Image.Ref + "|" + j.Services[0].Name }},
		"cache key":   {func(j *ir.Job) { j.Cache = []ir.Cache{{Key: "k-" + tpl, Paths: []string{"dir-" + tpl}}} }, func(j *ir.Job) string { return j.Cache[0].Key + "|" + j.Cache[0].Paths[0] }},
		"artifact":    {func(j *ir.Job) { j.Artifacts = []ir.Artifact{{Name: "a-" + tpl, Paths: []string{"out-" + tpl}}} }, func(j *ir.Job) string { return j.Artifacts[0].Name + "|" + j.Artifacts[0].Paths[0] }},
		"environment": {func(j *ir.Job) { j.Environment = &ir.Environment{Name: "e", URL: "https://x/" + tpl} }, func(j *ir.Job) string { return j.Environment.URL }},
		"workdir":     {func(j *ir.Job) { j.Steps[0].WorkDir = "d/" + tpl }, func(j *ir.Job) string { return j.Steps[0].WorkDir }},
		"runner":      {func(j *ir.Job) { j.Runner.Labels = []string{"l-" + tpl} }, func(j *ir.Job) string { return j.Runner.Labels[0] }},
	}
	for name, c := range sites {
		t.Run(name, func(t *testing.T) {
			j := glJob("build", glStep("make"))
			c.build(j)
			want := c.read(j)
			_, back, _ := emitParse(t, glProject("gitlab", j), nil)
			if got := c.read(back.Pipelines[0].Jobs[0]); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestRefsPipelineConcurrencyGroup(t *testing.T) {
	p := glProject("gitlab", glJob("build", glStep("make")), glJob("test", glStep("make")))
	p.Pipelines[0].Concurrency = &ir.Concurrency{Group: "deploy-${{ inputs.env }}"}
	_, back, _ := emitParse(t, p, nil)
	if g := back.Pipelines[0].Concurrency; g == nil || g.Group != "deploy-${{ inputs.env }}" {
		t.Errorf("concurrency = %+v", g)
	}
}

func TestRefsEditedTextIsNotRestored(t *testing.T) {
	st := glStep("echo ${{ matrix.x }}")
	_, back, rep := emitParse(t, glProject("gitlab", glJob("build", st)), func(s string) string {
		return strings.Replace(s, "- echo $x", "- echo $y", 1)
	})
	if got := back.Pipelines[0].Jobs[0].Steps[0].Script[0]; got != "echo $y" {
		t.Errorf("edited line = %q", got)
	}
	if !ignoredNote(rep) {
		t.Error("no marker ignored note")
	}
}
