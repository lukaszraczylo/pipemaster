package gitlab_test

import (
	"reflect"
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

// A flattening site that refPairs does not record is never silent.
func TestUnrecordedFlatteningSitesAreReported(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.Outputs = []ir.Output{{Name: "v", Value: "${{ matrix.os }}-x"}}
	p := glProject("gitlab", j)
	p.Pipelines[0].Inputs = []ir.Input{{Name: "t", Default: "a", Description: "d ${{ matrix.os }}"}}
	_, rep := func() (string, *report.Report) {
		_, rep, err := gitlab.New().Emit(p)
		if err != nil {
			t.Fatal(err)
		}
		return "", rep
	}()
	paths := map[string]bool{}
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "namespace flattened; restored only with round-trip metadata") {
			paths[e.IRPath] = true
		}
	}
	for _, want := range []string{"pipeline/gitlab-ci/job/build/output/v", "pipeline/gitlab-ci/input/t"} {
		if !paths[want] {
			t.Errorf("no entry at %s; got %v", want, paths)
		}
	}
}

func noIgnoredNotes(t *testing.T, rep *report.Report) {
	t.Helper()
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "marker ignored") {
			t.Errorf("unedited file produced %q at %s", e.Detail, e.IRPath)
		}
	}
}

func TestRefsRestoreServiceEnvAndHashedCacheKey(t *testing.T) {
	const tpl = "${{ matrix.os }}"
	j := glJob("build", glStep("make"))
	j.Services = []ir.Service{{Image: ir.Image{Ref: "pg:15"}, Name: "db", Env: []ir.Var{{Name: "K", Value: ir.Value{Lit: "v-" + tpl}}}}}
	j.Cache = []ir.Cache{{Key: "k-" + tpl + "-${{ hashFiles('go.sum') }}", Paths: []string{"vendor"}}}
	_, back, rep := emitParse(t, glProject("gitlab", j), nil)
	got := back.Pipelines[0].Jobs[0]
	if v := got.Services[0].Env[0].Value.Lit; v != "v-"+tpl {
		t.Errorf("service env = %q", v)
	}
	if k := got.Cache[0].Key; k != j.Cache[0].Key {
		t.Errorf("cache key = %q", k)
	}
	noIgnoredNotes(t, rep)
}

func TestRefsRestoreInsideExpandedMatrix(t *testing.T) {
	j := glJob("build", glStep("echo ${{ matrix.q }}"))
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "q", Values: []string{"a-${{ inputs.x }}", "b"}}},
		Include: []ir.Combo{{{Key: "q", Value: "c-${{ env.v }}"}}},
		Exclude: []ir.Combo{{{Key: "q", Value: "b"}}},
	}
	_, back, rep := emitParse(t, glProject("gitlab", j), nil)
	got := back.Pipelines[0].Jobs[0]
	if !reflect.DeepEqual(got.Matrix, j.Matrix) {
		t.Errorf("matrix = %+v, want %+v", got.Matrix, j.Matrix)
	}
	if s := got.Steps[0].Script[0]; s != "echo ${{ matrix.q }}" {
		t.Errorf("script = %q", s)
	}
	noIgnoredNotes(t, rep)
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "no parser rule restores job.matrix") {
			t.Errorf("matrix marker not collapsed")
		}
	}
}

func TestMultiAxisScriptLineRestoresFully(t *testing.T) {
	const line = "echo ${{ matrix.q }} ${{ matrix.r }}"
	j := glJob("build", glStep(line))
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "q", Values: []string{"a", "b"}}, {Name: "r", Values: []string{"1", "2"}}},
		Exclude: []ir.Combo{{{Key: "q", Value: "a"}, {Key: "r", Value: "1"}}},
	}
	_, back, rep := emitParse(t, glProject("gitlab", j), nil)
	if got := back.Pipelines[0].Jobs[0].Steps[0].Script[0]; got != line {
		t.Errorf("script = %q", got)
	}
	noIgnoredNotes(t, rep)
}

func producer(id string, outs ...string) *ir.Job {
	j := glJob(id, glStep("make"))
	for _, o := range outs {
		j.Outputs = append(j.Outputs, ir.Output{Name: o, Value: "x"})
	}
	return j
}

func consumer(line string, needs ...string) *ir.Job {
	j := glJob("use", glStep(line))
	for _, n := range needs {
		j.Needs = append(j.Needs, ir.Need{JobID: n})
	}
	return j
}

func roundTripLine(t *testing.T, line string, matrix bool, prods []*ir.Job, needs ...string) {
	t.Helper()
	c := consumer(line, needs...)
	if matrix {
		c.Matrix = &ir.Matrix{
			Axes:    []ir.Axis{{Name: "q", Values: []string{"a", "b"}}},
			Exclude: []ir.Combo{{{Key: "q", Value: "a"}}},
		}
	}
	for _, order := range []bool{false, true} {
		jobs := append([]*ir.Job{}, prods...)
		jobs = append(jobs, c)
		if order {
			jobs = append([]*ir.Job{c}, prods...)
		}
		_, back, rep := emitParse(t, glProject("gitlab", jobs...), nil)
		if got := jobNamed(back, "use").Steps[0].Script[0]; got != line {
			t.Errorf("order %v: script = %q, want %q", order, got, line)
		}
		noIgnoredNotes(t, rep)
	}
}

func TestMatrixAndOutputRefOnOneLine(t *testing.T) {
	roundTripLine(t, "echo ${{ matrix.q }} ${{ needs.prod.outputs.v }}", true, []*ir.Job{producer("prod", "v")}, "prod")
}

func TestTwoProducersOnOneLine(t *testing.T) {
	roundTripLine(t, "echo ${{ needs.p1.outputs.a }} ${{ needs.p2.outputs.b }}", false,
		[]*ir.Job{producer("p1", "a"), producer("p2", "b")}, "p1", "p2")
}

func TestMatrixAndTwoProducersOnOneLine(t *testing.T) {
	roundTripLine(t, "echo ${{ matrix.q }} ${{ needs.p1.outputs.a }} ${{ needs.p2.outputs.b }}", true,
		[]*ir.Job{producer("p1", "a"), producer("p2", "b")}, "p1", "p2")
}
