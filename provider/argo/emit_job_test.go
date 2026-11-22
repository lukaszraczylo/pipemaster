package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func template(t *testing.T, files map[string]string) doc {
	t.Helper()
	for name, src := range files {
		if strings.HasPrefix(name, ".argo/workflows/") {
			return docs(t, src)[0]
		}
	}
	t.Fatal("no workflow file")
	return nil
}

func tasks(t *testing.T, d doc) []any {
	t.Helper()
	return list(get(named(t, get(d, "spec", "templates"), "main"), "dag", "tasks"))
}

func TestNeedsOutputUsesDefault(t *testing.T) {
	build := scriptJob("build", "echo hi")
	build.Outputs = []ir.Output{{Name: "v"}}
	test := scriptJob("test", "echo ${{ needs.build.outputs.v }}")
	test.Needs = []ir.Need{{JobID: "build"}}
	files, _ := emit(t, project(build, test))
	task := named(t, tasks(t, template(t, files)), "test")
	arg := named(t, get(task, "arguments", "parameters"), "needs-build-v")
	if arg["value"] != "{{= (tasks['build'].outputs.parameters['v'] ?? '') }}" {
		t.Errorf("argument value = %v", arg["value"])
	}
	deps := list(task["dependencies"])
	if len(deps) != 1 || deps[0] != "build" {
		t.Errorf("dependencies = %v, want [build]", deps)
	}
}

func TestPlainNeedJoinsTheDependsOfADAGThatHasStatus(t *testing.T) {
	a, b, c := scriptJob("a", "x"), scriptJob("b", "x"), scriptJob("c", "x")
	b.Needs = []ir.Need{{JobID: "a"}}
	c.Needs = []ir.Need{{JobID: "a"}}
	c.If = eqStr([]string{"needs", "a", "result"}, "failure")
	files, _ := emit(t, project(a, b, c))
	ts := tasks(t, template(t, files))
	if tb := named(t, ts, "b"); tb["depends"] != "a.Succeeded" || tb["dependencies"] != nil {
		t.Errorf("b = %v", tb)
	}
	tc := named(t, ts, "c")
	if tc["dependencies"] != nil || tc["depends"] == nil {
		t.Errorf("c = %v, want depends and no dependencies", tc)
	}
	if !strings.Contains(tc["depends"].(string), "a.Failed") {
		t.Errorf("depends = %v", tc["depends"])
	}
}

func TestTaskNameCollisionsAreResolved(t *testing.T) {
	p := project(scriptJob("Build", "x"), scriptJob("build", "x"), scriptJob("a_b", "x"), scriptJob("a-b", "x"))
	files, rep := emit(t, p)
	seen := map[string]bool{}
	for _, tk := range tasks(t, template(t, files)) {
		n := tk.(doc)["name"].(string)
		if seen[n] {
			t.Errorf("task name %q is used twice", n)
		}
		seen[n] = true
	}
	if len(seen) != 4 {
		t.Errorf("tasks = %v", seen)
	}
	if len(entries(rep, "collides")) == 0 {
		t.Error("no report entry for the collision")
	}
	back := roundTrip(t, files)
	if len(back.Pipelines[0].Jobs) != 4 {
		t.Errorf("round trip jobs = %d", len(back.Pipelines[0].Jobs))
	}
	for i, j := range back.Pipelines[0].Jobs {
		if j.ID != p.Pipelines[0].Jobs[i].ID {
			t.Errorf("job %d id = %q, want %q", i, j.ID, p.Pipelines[0].Jobs[i].ID)
		}
	}
}

func TestMatrixCellsWithTagsAndQuotes(t *testing.T) {
	j := scriptJob("test", "echo ${{ matrix.v }}")
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "v", Values: []string{"a'b", "c\"d", "{{x}}", "plain"}}}}
	files, rep := emit(t, project(j))
	task := named(t, tasks(t, template(t, files)), "test")
	items := list(task["withItems"])
	if len(items) != 3 {
		t.Fatalf("withItems = %v, want the three cells without a tag", items)
	}
	if len(entries(rep, "holds {{")) == 0 {
		t.Error("no report entry for the dropped cell")
	}
	if strings.Contains(joinFiles(files), "pipemaster:emulated job.matrix pipeline/ci/job/test\n# pipemaster:arg") {
		t.Error("a matrix marker was written for a partial matrix")
	}
}

func TestMatrixExcludeRemovesExactlyTheExcludedCells(t *testing.T) {
	j := scriptJob("test", "echo ${{ matrix.os }} ${{ matrix.go }}")
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "os", Values: []string{"linux", "mac"}}, {Name: "go", Values: []string{"1", "2"}}},
		Exclude: []ir.Combo{{{Key: "os", Value: "mac"}, {Key: "go", Value: "1"}}},
	}
	p := project(j)
	files, _ := emit(t, p)
	items := list(named(t, tasks(t, template(t, files)), "test")["withItems"])
	if len(items) != 3 {
		t.Fatalf("withItems = %v, want 3 cells", items)
	}
	for _, it := range items {
		m := it.(doc)
		if m["os"] == "mac" && m["go"] == "1" {
			t.Errorf("the excluded cell is present: %v", m)
		}
	}
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("matrix trip: %s", diff)
	}
}

func TestEditedMatrixCellsAreKeptAndMarkerIgnored(t *testing.T) {
	j := scriptJob("test", "echo ${{ matrix.os }}")
	j.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"linux", "mac"}}}}
	files, _ := emit(t, project(j))
	for n, s := range files {
		files[n] = strings.Replace(s, "os: mac", "os: windows", 1)
	}
	p, rep, err := parseRepo(files)
	if err != nil {
		t.Fatal(err)
	}
	job := p.Pipelines[0].Jobs[0]
	if job.Matrix != nil {
		t.Error("the edited cells became a matrix")
	}
	if !strings.Contains(joinExt(job), "windows") {
		t.Errorf("the edited cells are lost: %v", job.Ext)
	}
	if len(entries(rep, "marker ignored")) == 0 {
		t.Error("no marker ignored entry")
	}
}

func TestGateForManualAndApproval(t *testing.T) {
	a, b := scriptJob("a", "x"), scriptJob("b", "x")
	b.Needs = []ir.Need{{JobID: "a"}}
	b.Manual = true
	b.Environment = &ir.Environment{Name: "prod", Approval: true}
	p := project(a, b)
	files, _ := emit(t, p)
	ts := tasks(t, template(t, files))
	gate := named(t, ts, "b-gate")
	if deps := list(gate["dependencies"]); len(deps) != 1 || deps[0] != "a" {
		t.Errorf("gate dependencies = %v", deps)
	}
	if deps := list(named(t, ts, "b")["dependencies"]); len(deps) != 1 || deps[0] != "b-gate" {
		t.Errorf("b dependencies = %v", deps)
	}
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("gate trip: %s", diff)
	}
}
