package woodpecker

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

func markerText(f caps.Feature, path, arg string) []byte {
	s := "# pipemaster:emulated " + string(f) + " " + path + "\n"
	if arg != "" {
		s += "# pipemaster:arg " + arg + "\n"
	}
	return []byte(s)
}

func twoSteps() *ir.Job {
	return &ir.Job{ID: "ci", Steps: []*ir.Step{
		{ID: "a", Script: []string{"echo ${TOKEN}"}},
		{ID: "b", Script: []string{"echo b"}},
	}}
}

func TestCollapseJobRejectsMalformedMarkers(t *testing.T) {
	const jp = "pipeline/woodpecker/job/ci"
	env := `"- name: X\n  value:\n    lit: \"1\"\n"`
	cases := []struct {
		name    string
		feature caps.Feature
		path    string
		arg     string
	}{
		{"refs not json", caps.ScriptInterpolation, jp, `{not json`},
		{"refs negative pos", caps.ScriptInterpolation, jp, `{"refs":["secrets.A"],"pos":-1}`},
		{"refs pos out of range", caps.ScriptInterpolation, jp, `{"refs":["secrets.A"],"pos":2}`},
		{"refs without namespace", caps.ScriptInterpolation, jp, `{"refs":["A"],"pos":0}`},
		{"name negative pos", caps.DisplayName, jp + "/step/x", `{"name":"n","pos":-4}`},
		{"name out of range", caps.DisplayName, jp + "/step/x", `{"name":"n","pos":9}`},
		{"name without step in path", caps.DisplayName, jp, `{"name":"n","pos":0}`},
		{"lost env negative pos", caps.ExprOpaque, jp, `{"vars":` + env + `,"at":[0],"pos":-1}`},
		{"lost env out of range", caps.ExprOpaque, jp, `{"vars":` + env + `,"at":[0],"pos":7}`},
		{"lost env negative at", caps.ExprOpaque, jp, `{"vars":` + env + `,"at":[-2],"pos":0}`},
		{"lost env count mismatch", caps.ExprOpaque, jp, `{"vars":` + env + `,"at":[],"pos":0}`},
		{"action negative at", caps.StepAction, jp + "/step/c", `{"step":"id: c\nkind: action\n","at":-3}`},
		{"action not yaml", caps.StepAction, jp + "/step/c", `{"step":"[: x","at":0}`},
		{"matrix not json", caps.JobMatrix, jp, `[1`},
		{"env not a list", caps.JobEnv, jp, `"a: [b"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := twoSteps()
			before := fmt.Sprintf("%+v %+v", *j.Steps[0], *j.Steps[1])
			rep := &report.Report{}
			collapseJob(rep, j, readMarks(markerText(c.feature, c.path, c.arg)))
			if len(j.Steps) != 2 || fmt.Sprintf("%+v %+v", *j.Steps[0], *j.Steps[1]) != before {
				t.Errorf("a rejected marker changed the job: %+v", j.Steps)
			}
			found := false
			for _, e := range rep.Entries {
				found = found || strings.Contains(e.Detail, "marker not collapsed") && e.Feature == c.feature
			}
			if !found {
				t.Errorf("no uncollapsed entry: %+v", rep.Entries)
			}
		})
	}
}

func TestCollapseJobRestoresEachMarkerKind(t *testing.T) {
	const jp = "pipeline/woodpecker/job/ci"
	env := `"- name: X\n  value:\n    lit: \"1\"\n"`
	cases := []struct {
		check   func(*ir.Job) bool
		name    string
		feature caps.Feature
		path    string
		arg     string
	}{
		{func(j *ir.Job) bool { return j.Steps[0].Script[0] == "echo ${{ secrets.TOKEN }}" },
			"refs", caps.ScriptInterpolation, jp, `{"refs":["secrets.TOKEN"],"pos":0}`},
		{func(j *ir.Job) bool { return j.Steps[1].Name == "b" && j.Steps[1].ID == "orig" },
			"display name", caps.DisplayName, jp + "/step/orig", `{"name":"b","pos":1}`},
		{func(j *ir.Job) bool { return j.Container != nil && j.Container.Ref == "alpine" }, "container", caps.JobContainer, jp, `{"Ref":"alpine"}`},
		{func(j *ir.Job) bool { return j.Matrix != nil && len(j.Matrix.Axes) == 1 }, "matrix", caps.JobMatrix, jp, `{"Axes":[{"Name":"go","Values":["1"]}]}`},
		{func(j *ir.Job) bool { return j.AllowFailure }, "allow failure", caps.JobAllowFailure, jp, ``},
		{func(j *ir.Job) bool { return len(j.Steps[1].Env) == 1 && j.Steps[1].Env[0].Name == "X" },
			"lost env", caps.ExprOpaque, jp, `{"vars":` + env + `,"at":[0],"pos":1}`},
		{func(j *ir.Job) bool { return len(j.Steps) == 3 && j.Steps[1].ID == "c" },
			"action", caps.StepAction, jp + "/step/c", `{"step":"id: c\nkind: action\n","at":1}`},
		{func(j *ir.Job) bool { return len(j.Env) == 1 && j.Env[0].Name == "X" }, "job env", caps.JobEnv, jp, env},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := twoSteps()
			rep := &report.Report{}
			collapseJob(rep, j, readMarks(markerText(c.feature, c.path, c.arg)))
			if !c.check(j) {
				t.Errorf("marker not applied: %+v entries %+v", j, rep.Entries)
			}
			for _, e := range rep.Entries {
				if strings.Contains(e.Detail, "marker not collapsed") {
					t.Errorf("good marker reported: %+v", e)
				}
			}
		})
	}
}

func TestRenameKeepsToItsOwnJobPaths(t *testing.T) {
	multi := func(job string) *workflow {
		arg := fmt.Sprintf(`{"pipeline":"ci","job":"%s","p":0,"j":0}`, job)
		return &workflow{job: &ir.Job{ID: job + "-wf"}, marks: readMarks(markerText(caps.ProjectMultiPipeline, "project", arg))}
	}
	g := &group{id: "ci", wfs: []*workflow{multi("a"), multi("ab")}}
	g.wfs[0].job.ID, g.wfs[1].job.ID = "x", "x2"
	rep := &report.Report{}
	for _, p := range []string{"x", "x/step/s", "x2", "x2/step/s"} {
		rep.Add(report.Entry{IRPath: pipelinePth + "/job/" + p, Feature: caps.StepIf, Level: caps.Lost})
	}
	g.rename(rep, &ir.Pipeline{ID: "ci", Jobs: []*ir.Job{g.wfs[0].job, g.wfs[1].job}})
	var got []string
	for _, e := range rep.Entries {
		got = append(got, e.IRPath)
	}
	want := []string{"pipeline/ci/job/a", "pipeline/ci/job/a/step/s", "pipeline/ci/job/ab", "pipeline/ci/job/ab/step/s"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}
