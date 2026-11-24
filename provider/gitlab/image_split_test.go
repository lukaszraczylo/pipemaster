package gitlab_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"

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
	if strings.Count(doc, "the later steps are skipped") != 2 || strings.Count(doc, "touch \"$CI_PROJECT_DIR\"/'.pipemaster-split/") != 2 {
		t.Errorf("an allowed failure needs a guard in jobs 2 and 3 and a marker in jobs 1 and 2:\n%s", doc)
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
	for _, w := range []string{"artifact size limit", "outside the project directory", "services and variables", "deletes comes back", "timeout times the job count", "retry reruns only the failed job", "marker file"} {
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

// runParts runs the script lines of the named jobs in order in one directory, as bash -e, and returns the exit codes.
func runParts(t *testing.T, doc string, ids ...string) ([]int, string) {
	t.Helper()
	return runPartsIn(t, t.TempDir(), doc, ids...)
}

func runPartsIn(t *testing.T, dir, doc string, ids ...string) ([]int, string) {
	t.Helper()
	var jobs map[string]any
	if err := yaml.Unmarshal([]byte(doc), &jobs); err != nil {
		t.Fatal(err)
	}
	var codes []int
	for _, id := range ids {
		var lines []string
		for _, l := range jobs[id].(map[string]any)["script"].([]any) {
			lines = append(lines, l.(string))
		}
		cmd := exec.Command("bash", "-e", "-c", strings.Join(lines, "\n")) //nolint:gosec // the script is emitted by the test
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CI_PROJECT_DIR="+dir)
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		codes = append(codes, code)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log")) //nolint:gosec // a path in the test temp dir; no log means no step ran
	return codes, string(log)
}

func failingChain(allow bool) *ir.Project {
	j := glJob("build", imgStep("a", "go:1", "echo a >> log"), imgStep("b", "node:20", "false", "echo b >> log"), imgStep("c", "go:1", "echo c >> log"))
	j.AllowFailure = allow
	return glProject("drone", j)
}

func TestImageSplitFailedMiddleJobSkipsLaterJobs(t *testing.T) {
	doc := emitDoc(t, failingChain(true))
	if !strings.Contains(doc, "pipemaster: build-2 failed; the source job stops here, so the later steps are skipped") {
		t.Fatalf("no guard message:\n%s", doc)
	}
	codes, log := runParts(t, doc, "build-1", "build-2", "build-3")
	if log != "a\n" {
		t.Errorf("steps after the failed job ran, log = %q", log)
	}
	if codes[0] != 0 || codes[1] == 0 || codes[2] != 0 {
		t.Fatalf("exit codes = %v, want the failed middle job to fail and the last to end with success", codes)
	}
}

func TestImageSplitGuardSkipsOnlyAfterAFailure(t *testing.T) {
	ok := glProject("drone", func() *ir.Job {
		j := glJob("build", imgStep("a", "go:1", "echo a >> log"), imgStep("b", "node:20", "echo b >> log"), imgStep("c", "go:1", "test \"$(cat log)\" = \"$(printf 'a\\nb')\" && echo c >> log"))
		j.AllowFailure = true
		return j
	}())
	doc := emitDoc(t, ok)
	if codes, _ := runParts(t, doc, "build-1", "build-2", "build-3"); slices.Max(codes) != 0 {
		t.Errorf("a chain without a failure must run every step in order: %v", codes)
	}
}

func TestImageSplitSkippedJobSkipsTheNext(t *testing.T) {
	j := glJob("build", imgStep("a", "go:1", "false"), imgStep("b", "node:20", "echo b >> log"), imgStep("c", "go:1", "echo c >> log"))
	j.AllowFailure = true
	doc := emitDoc(t, glProject("drone", j))
	if codes, log := runParts(t, doc, "build-1", "build-2", "build-3"); codes[0] == 0 || codes[1] != 0 || codes[2] != 0 || log != "" {
		t.Errorf("exit codes = %v, log = %q", codes, log)
	}
}

func TestImageSplitNeedsNoGuardWithoutAllowFailure(t *testing.T) {
	doc := emitDoc(t, failingChain(false))
	for _, w := range []string{"the later steps are skipped", ".pipemaster-split", "allow_failure"} {
		if strings.Contains(doc, w) {
			t.Errorf("a job that must succeed needs no %q:\n%s", w, doc)
		}
	}
	if !strings.Contains(doc, "- build-2") {
		t.Errorf("the chain must keep its needs:\n%s", doc)
	}
}

func TestImageSplitMarkerSurvivesAStepThatChangesDirectory(t *testing.T) {
	j := glJob("build", imgStep("a", "go:1", "mkdir sub", "cd sub", "echo a >> ../log"), imgStep("b", "node:20", "echo b >> log"), imgStep("c", "go:1", "echo c >> log"))
	j.AllowFailure = true
	doc := emitDoc(t, glProject("drone", j))
	if codes, log := runParts(t, doc, "build-1", "build-2", "build-3"); slices.Max(codes) != 0 || log != "a\nb\nc\n" {
		t.Errorf("a cd in one job must not hide its marker: codes %v, log %q", codes, log)
	}
}

func TestImageSplitStaleMarkerDoesNotLetALaterJobRun(t *testing.T) {
	doc := emitDoc(t, failingChain(true))
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".pipemaster-split"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pipemaster-split", markFileOf(t, doc, "build-2")), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	codes, log := runPartsIn(t, dir, doc, "build-1", "build-2", "build-3")
	if log != "a\n" || codes[1] == 0 || codes[2] != 0 {
		t.Errorf("an old marker let the last job run: codes %v, log %q", codes, log)
	}
}

func TestImageSplitHidesMarkersFromTheSourceArtifacts(t *testing.T) {
	j := glJob("build", imgStep("a", "go:1", "x"), imgStep("b", "node:20", "y"))
	j.AllowFailure = true
	j.Artifacts = []ir.Artifact{{Name: "out", Paths: []string{"bin/"}}}
	pl, _, doc := splitEmitParse(t, glProject("drone", j))
	if !strings.Contains(doc, ".pipemaster-split/**/*") {
		t.Errorf("the last job must exclude the marker directory:\n%s", doc)
	}
	if b := job(t, pl, "build"); len(b.Artifacts) != 1 || b.Ext["gitlab"] != nil && strings.Contains(fmt.Sprint(b.Ext["gitlab"]), "pipemaster-split") {
		t.Errorf("the exclude leaked into the joined job: %+v", b.Ext)
	}
}

func markFileOf(t *testing.T, doc, id string) string {
	t.Helper()
	m := regexp.MustCompile(`'\.pipemaster-split/(` + regexp.QuoteMeta(id) + `-[0-9a-f]{8}\.ok)'`).FindStringSubmatch(doc)
	if m == nil {
		t.Fatalf("no marker file for %s:\n%s", id, doc)
	}
	return m[1]
}

func TestImageSplitMarkerNamesSurviveAnyJobName(t *testing.T) {
	for _, name := range []string{"rspec 1/3", "a b", "ü", "a/b", "a_b", "x'y", "$(touch pwn)"} {
		t.Run(name, func(t *testing.T) {
			j := glJob(name, imgStep("a", "go:1", "echo a >> log"), imgStep("b", "node:20", "false"), imgStep("c", "go:1", "echo c >> log"))
			j.AllowFailure = true
			doc := emitDoc(t, glProject("drone", j))
			first := name + "-1"
			if m := regexp.MustCompile(`\.pipemaster-split/([^']*)'`).FindAllStringSubmatch(doc, -1); len(m) == 0 {
				t.Fatalf("no marker in:\n%s", doc)
			} else {
				for _, x := range m {
					if strings.ContainsAny(x[1], "/ '$") || x[1] == "" {
						t.Errorf("unsafe marker name %q", x[1])
					}
				}
			}
			codes, log := runParts(t, doc, first, name+"-2", name+"-3")
			if codes[0] != 0 || codes[1] == 0 || codes[2] != 0 || log != "a\n" {
				t.Errorf("codes %v, log %q: the first job must pass, the second fail, the last skip", codes, log)
			}
			if _, err := os.Stat("pwn"); err == nil {
				t.Error("a job name ran as a command")
			}
		})
	}
}

func TestImageSplitNamesThatSpellTheSameKeepSeparateMarkers(t *testing.T) {
	a := markFileOf(t, emitDoc(t, failingChain(true)), "build-2")
	if !regexp.MustCompile(`^build-2-[0-9a-f]{8}\.ok$`).MatchString(a) {
		t.Fatalf("marker name %q", a)
	}
	split := func(name string) string {
		j := glJob(name, imgStep("x", "go:1", "x"), imgStep("y", "node:20", "y"))
		j.AllowFailure = true
		return emitDoc(t, glProject("drone", j))
	}
	slash, under := split("a/b"), split("a_b")
	slashM := regexp.MustCompile(`a_b-1-[0-9a-f]{8}`).FindString(slash)
	underM := regexp.MustCompile(`a_b-1-[0-9a-f]{8}`).FindString(under)
	if slashM == "" || underM == "" || slashM == underM {
		t.Errorf("markers %q and %q must differ, or a_b-1 and a/b-1 would share a file", slashM, underM)
	}
}
