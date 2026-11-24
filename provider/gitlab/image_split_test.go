package gitlab_test

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
)

func imgStep(id, image string, lines ...string) *ir.Step {
	st := &ir.Step{ID: id, Kind: ir.StepScript, Script: lines}
	if image != "" {
		st.Image = &ir.Image{Ref: image}
	}
	return st
}

func splitStepIDs(j *ir.Job) []string {
	var out []string
	for _, st := range j.Steps {
		out = append(out, st.ID)
	}
	return out
}

func splitJobIDs(pl *ir.Pipeline) []string {
	var out []string
	for _, j := range pl.Jobs {
		out = append(out, j.ID)
	}
	return out
}

func lostImage(rep *report.Report) []string {
	var out []string
	for _, e := range rep.Entries {
		if e.Feature == caps.StepImage && e.Level == caps.Lost {
			out = append(out, e.IRPath+": "+e.Detail)
		}
	}
	return out
}

func splitEmitParse(t *testing.T, p *ir.Project) (*ir.Pipeline, *report.Report, string) {
	t.Helper()
	files, rep := emitFiles(t, p)
	back, _, err := gitlab.New().Parse(fstest.MapFS{rootFile: &fstest.MapFile{Data: files[rootFile]}})
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, files[rootFile])
	}
	return back.Pipelines[0], rep, string(files[rootFile])
}

func TestImageSplitChainsJobsAndJoinsThemBack(t *testing.T) {
	build := glJob("build", imgStep("a", "go:1", "make a"), imgStep("b", "go:1", "make b"), imgStep("c", "node:20", "npm t"), imgStep("d", "go:1", "make d"))
	build.Env = []ir.Var{{Name: "MODE", Value: ir.Value{Lit: "fast"}}}
	build.Services = []ir.Service{{Name: "db", Image: ir.Image{Ref: "postgres:16"}}}
	build.AllowFailure = true
	deploy := glJob("deploy", imgStep("ship", "", "make ship"))
	deploy.Needs = []ir.Need{{JobID: "build"}}
	pl, rep, doc := splitEmitParse(t, glProject("drone", build, deploy))
	if lost := lostImage(rep); len(lost) > 0 {
		t.Errorf("images lost: %v", lost)
	}
	for _, want := range []string{"build-1:", "build-2:", "build-3:", "image: go:1", "image: node:20", "- build-3", "- .\n", "- .git/**/*"} {
		if !strings.Contains(doc, want) {
			t.Errorf("output lacks %q:\n%s", want, doc)
		}
	}
	if strings.Count(doc, "MODE: fast") != 3 || strings.Count(doc, "postgres:16") != 3 {
		t.Errorf("env and services must repeat in every split job:\n%s", doc)
	}
	if strings.Count(doc, "allow_failure: true") != 3 || strings.Contains(doc, "when: on_failure") || strings.Contains(doc, "when: always") {
		t.Errorf("allow_failure must be on every split job, with the default when so a failed job stops its needs chain:\n%s", doc)
	}
	if got := splitJobIDs(pl); !slices.Equal(got, []string{"build", "deploy"}) {
		t.Fatalf("jobs after the return = %v", got)
	}
	b := job(t, pl, "build")
	if got := splitStepIDs(b); !slices.Equal(got, []string{"a", "b", "c", "d"}) {
		t.Errorf("steps = %v", got)
	}
	for _, st := range b.Steps {
		want := map[string]string{"a": "go:1", "b": "go:1", "c": "node:20", "d": "go:1"}[st.ID]
		if st.Image == nil || st.Image.Ref != want {
			t.Errorf("step %s image = %v, want %s", st.ID, st.Image, want)
		}
	}
	if b.Container != nil || !b.AllowFailure || len(b.Env) != 1 || len(b.Artifacts) != 0 {
		t.Errorf("joined job differs from the source: %+v", b)
	}
	if got := needIDs(job(t, pl, "deploy")); !slices.Equal(got, []string{"build"}) {
		t.Errorf("deploy needs = %v", got)
	}
}

func TestImageSplitReportsEmulationOnEveryStep(t *testing.T) {
	_, rep := emitFiles(t, glProject("drone", glJob("build", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"))))
	var details []string
	for _, e := range rep.Entries {
		if e.Feature == caps.StepImage && e.Level == caps.Emulated {
			details = append(details, e.Detail)
		}
	}
	if len(details) != 2 {
		t.Fatalf("want 2 emulated entries, got %v", details)
	}
	for _, w := range []string{"artifact size limit", "outside the project directory", "services and variables", "deletes comes back", "timeout times the job count", "retry reruns only the failed job", "still starts"} {
		if !strings.Contains(details[0], w) {
			t.Errorf("caveat lacks %q: %s", w, details[0])
		}
	}
}

func TestImageSplitLeavesWhatItCannotSplit(t *testing.T) {
	cleanup := imgStep("clean", "go:1", "rm -rf out")
	cleanup.If = expr.Call{Name: "always"}
	manualAllowed := glJob("m", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"))
	manualAllowed.Manual, manualAllowed.AllowFailure = true, true
	matrix := glJob("mx", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"))
	matrix.Matrix = &ir.Matrix{Axes: []ir.Axis{{Name: "os", Values: []string{"a", "b"}}}}
	cases := map[string]struct {
		job *ir.Job
		why string
	}{
		"cleanup after an image change": {glJob("c", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"), cleanup), "must run after a failure"},
		"manual and allowed to fail":    {manualAllowed, "manual and allowed to fail"},
		"matrix":                        {matrix, "matrix"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pl, rep, _ := splitEmitParse(t, glProject("drone", c.job))
			if len(pl.Jobs) != 1 {
				t.Errorf("job was split: %v", splitJobIDs(pl))
			}
			lost := lostImage(rep)
			if len(lost) == 0 || !strings.Contains(strings.Join(lost, "\n"), c.why) {
				t.Errorf("want a lost verdict that names %q, got %v", c.why, lost)
			}
		})
	}
}

func TestImageSplitSingleImageBecomesTheJobImage(t *testing.T) {
	j := glJob("build", imgStep("a", "go:1", "x"), imgStep("b", "go:1", "y"))
	pl, rep, doc := splitEmitParse(t, glProject("drone", j))
	if !strings.Contains(doc, "image: go:1") || strings.Contains(doc, "artifacts") {
		t.Errorf("single image job:\n%s", doc)
	}
	if len(lostImage(rep)) > 0 {
		t.Errorf("lost: %v", lostImage(rep))
	}
	b := job(t, pl, "build")
	if b.Container != nil || b.Steps[0].Image == nil || b.Steps[1].Image == nil {
		t.Errorf("the return trip did not move the image back to the steps: %+v", b)
	}
}

func TestImageSplitPartNamesAvoidExistingJobs(t *testing.T) {
	split := glJob("build", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"))
	pl, _, doc := splitEmitParse(t, glProject("drone", split, glJob("build-2", imgStep("s", "", "z"))))
	if got := splitJobIDs(pl); !slices.Equal(got, []string{"build", "build-2"}) {
		t.Errorf("jobs after the return = %v\n%s", got, doc)
	}
}

func TestImageSplitManualOnlyFirstJob(t *testing.T) {
	j := glJob("build", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"))
	j.Manual = true
	pl, _, doc := splitEmitParse(t, glProject("drone", j))
	if strings.Count(doc, "when: manual") != 1 {
		t.Errorf("only the first job is manual:\n%s", doc)
	}
	if b := job(t, pl, "build"); !b.Manual {
		t.Errorf("joined job lost manual")
	}
}

func TestImageSplitEditedChainStaysSeparate(t *testing.T) {
	files, _ := emitFiles(t, glProject("drone", glJob("build", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"))))
	edited := strings.Replace(string(files[rootFile]), "build-2:", "renamed:", 1)
	edited = strings.ReplaceAll(edited, "- build-1", "- build-1")
	back, rep, err := gitlab.New().Parse(fstest.MapFS{rootFile: &fstest.MapFile{Data: []byte(edited)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := splitJobIDs(back.Pipelines[0]); len(got) != 2 || slices.Contains(got, "build") {
		t.Errorf("an edited chain must not join: %v", got)
	}
	found := false
	for _, e := range rep.Entries {
		found = found || (e.Feature == caps.StepImage && strings.Contains(e.Detail, "marker not collapsed"))
	}
	if !found {
		t.Errorf("no report entry for the unjoined chain: %+v", rep.Entries)
	}
}
