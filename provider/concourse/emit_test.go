package concourse_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
)

func TestEmitNilIsNoError(t *testing.T) {
	files, rep, err := concourse.New().Emit(nil)
	if err != nil || len(files) != 0 || rep == nil {
		t.Fatalf("Emit(nil) = %v %v %v", files, rep, err)
	}
}

func TestPushTriggerBecomesTriggeringGet(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, rep := emit(t, p)
	d := pipelineDoc(t, files)
	src := resourceDoc(d, "source")
	if src == nil || src["type"] != "git" {
		t.Fatalf("resources = %v", d["resources"])
	}
	s := dict(src["source"])
	if s["branch"] != "main" || s["uri"] != "((git-uri))" {
		t.Fatalf("source = %v", s)
	}
	plan := list(jobDoc(t, d, "build")["plan"])
	get := dict(plan[0])
	if get["get"] != "source" || get["trigger"] != true {
		t.Fatalf("first step = %v", plan[0])
	}
	if !hasFeatureEntry(rep, string(caps.ExtPassthrough), "git-uri") {
		t.Fatalf("no entry that tells the operator to set git-uri: %v", rep.Entries)
	}
}

func TestDefaultImageIsReported(t *testing.T) {
	files, rep := emit(t, project(scriptJob("build", "make")))
	task := dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])
	img := dict(dict(task["config"])["image_resource"])
	src := dict(img["source"])
	if img["type"] != "registry-image" || src["repository"] != "ubuntu" || src["tag"] != "latest" {
		t.Fatalf("image_resource = %v", img)
	}
	if !hasFeatureEntry(rep, string(caps.StepImage), "ubuntu") {
		t.Fatalf("no image entry: %v", rep.Entries)
	}
}

func TestStepImageKeepsRepositoryAndTag(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Image = &ir.Image{Ref: "golang:1.22"}
	files, _ := emit(t, project(j))
	task := dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])
	src := dict(dict(dict(task["config"])["image_resource"])["source"])
	if src["repository"] != "golang" || src["tag"] != "1.22" {
		t.Fatalf("source = %v", src)
	}
}

func TestNeedsAreAPassedGetOfTheSharedSource(t *testing.T) {
	a, b := scriptJob("a", "true"), scriptJob("b", "true")
	c := scriptJob("c", "true")
	c.Needs = []ir.Need{{JobID: "a"}, {JobID: "b"}}
	p := project(a, b, c)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, _ := emit(t, p)
	get := dict(list(jobDoc(t, pipelineDoc(t, files), "c")["plan"])[0])
	if get["get"] != "source" || get["trigger"] != true {
		t.Fatalf("get = %v", get)
	}
	passed := list(get["passed"])
	if len(passed) != 2 || passed[0] != "a" || passed[1] != "b" {
		t.Fatalf("passed = %v", get["passed"])
	}
	back, _ := reparse(t, files)
	got := back.Pipelines[0].Jobs[2]
	if got.ID != "c" || len(got.Needs) != 2 || got.Manual {
		t.Fatalf("parsed back %+v", got)
	}
}

func TestNeedOnAJobWithNoSourceGetStillOrdersJobs(t *testing.T) {
	a := &ir.Job{ID: "a"}
	b := scriptJob("b", "true")
	b.Needs = []ir.Need{{JobID: "a"}}
	files, _ := emit(t, project(a, b))
	d := pipelineDoc(t, files)
	for _, name := range []string{"a", "b"} {
		get := dict(list(jobDoc(t, d, name)["plan"])[0])
		if get["get"] != "source" {
			t.Fatalf("job %s does not get the shared source: %v", name, get)
		}
	}
	back, _ := reparse(t, files)
	var got *ir.Job
	for _, j := range back.Pipelines[0].Jobs {
		if j.ID == "b" {
			got = j
		}
	}
	if got == nil || len(got.Needs) != 1 || got.Needs[0].JobID != "a" {
		t.Fatalf("the need did not come back: %+v", got)
	}
}

func TestTwoBranchPushIsTwoResources(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main", "dev")}
	files, rep := emit(t, p)
	d := pipelineDoc(t, files)
	for _, n := range []string{"source-main", "source-dev"} {
		if resourceDoc(d, n) == nil {
			t.Fatalf("no resource %s in %v", n, d["resources"])
		}
	}
	plan := list(jobDoc(t, d, "build")["plan"])
	triggering := 0
	for _, s := range plan {
		if m := dict(s); m["get"] != nil && m["trigger"] == true {
			triggering++
		}
	}
	if triggering != 2 {
		t.Fatalf("want two triggering gets: %v", plan)
	}
	if !hasFeatureEntry(rep, string(caps.TriggerFilterBranch), "once per branch event") {
		t.Fatalf("no per-branch entry: %v", rep.Entries)
	}
}

func TestBranchGlobIsLost(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("release/*", "main")}
	files, rep := emit(t, p)
	d := pipelineDoc(t, files)
	if resourceDoc(d, "source-main") == nil && resourceDoc(d, "source") == nil {
		t.Fatalf("the exact branch has no resource: %v", d["resources"])
	}
	for _, r := range list(d["resources"]) {
		if strings.Contains(dict(dict(r)["source"])["branch"].(string), "*") {
			t.Fatalf("a glob reached a resource: %v", r)
		}
	}
	if !hasFeatureEntry(rep, string(caps.TriggerFilterBranch), "release/*") {
		t.Fatalf("the glob is not reported: %v", rep.Entries)
	}
}

func TestManualJobHasNoTriggeringGet(t *testing.T) {
	j := scriptJob("deploy", "true")
	j.Manual = true
	p := project(j)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, _ := emit(t, p)
	for _, s := range list(jobDoc(t, pipelineDoc(t, files), "deploy")["plan"]) {
		if dict(s)["trigger"] == true {
			t.Fatalf("a manual job triggers: %v", s)
		}
	}
	back, _ := reparse(t, files)
	if !back.Pipelines[0].Jobs[0].Manual {
		t.Fatal("manual did not come back")
	}
}

func TestWorkspaceHandOff(t *testing.T) {
	j := &ir.Job{ID: "build", Steps: []*ir.Step{
		{ID: "one", Kind: ir.StepScript, Script: []string{"echo hi > out.txt"}},
		{ID: "two", Kind: ir.StepScript, Script: []string{"cat out.txt"}},
	}}
	files, _ := emit(t, project(j))
	plan := list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])
	one, two := dict(plan[1]), dict(plan[2])
	if dict(one["input_mapping"])["workspace"] != "source" || dict(two["input_mapping"]) != nil {
		t.Fatalf("input_mapping: first %v second %v", one["input_mapping"], two["input_mapping"])
	}
	build := t.TempDir()
	if err := os.Mkdir(filepath.Join(build, "workspace"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, task := range []map[string]any{one, two} {
		cfg := dict(task["config"])
		if len(list(cfg["inputs"])) != 1 || len(list(cfg["outputs"])) != 1 || dict(list(cfg["outputs"])[0])["name"] != "workspace" {
			t.Fatalf("inputs/outputs of %v", task)
		}
		run := dict(cfg["run"])
		args := list(run["args"])
		dir, _ := run["dir"].(string)
		if dir != "workspace" {
			t.Fatalf("run.dir = %q, want the workspace checkout", dir)
		}
		cmd := exec.Command("sh", "-ec", args[len(args)-1].(string)) //nolint:gosec // emitter output under test
		cmd.Dir = filepath.Join(build, dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("task %v failed: %v\n%s", task["task"], err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(build, "workspace", "out.txt")); err != nil {
		t.Fatalf("a file of step one is not in the workspace output: %v", err)
	}
}

func TestStepWithoutWorkdirReadsBackWithoutOne(t *testing.T) {
	files, _ := emit(t, project(scriptJob("build", "make")))
	back, _ := reparse(t, files)
	if got := back.Pipelines[0].Jobs[0].Steps[0].WorkDir; got != "" {
		t.Fatalf("workdir came back as %q", got)
	}
}

func TestWorkdirReadsBackWithoutMarkers(t *testing.T) {
	for _, dir := range []string{"", "app"} {
		j := scriptJob("build", "make")
		j.Steps[0].WorkDir = dir
		files, _ := emit(t, project(j))
		var kept []string
		for _, l := range strings.Split(files[".concourse/ci.yml"], "\n") {
			if !strings.HasPrefix(l, "# pipemaster:") {
				kept = append(kept, l)
			}
		}
		back, _ := parseFiles(t, map[string]string{"ci.yml": strings.Join(kept, "\n")})
		if got := back.Pipelines[0].Jobs[0].Steps[0].WorkDir; got != dir {
			t.Fatalf("workdir %q came back as %q", dir, got)
		}
	}
}

func TestZeroScriptJobWritesNoWorkspace(t *testing.T) {
	files, _ := emit(t, project(&ir.Job{ID: "empty"}))
	if strings.Contains(files[".concourse/ci.yml"], "workspace") {
		t.Fatalf("a job with no script step mentions the workspace:\n%s", files[".concourse/ci.yml"])
	}
}

func TestEnvAndSecretsGoToParams(t *testing.T) {
	j := scriptJob("build", "echo ${{ secrets.DEPLOY }} ${{ vars.region }}")
	j.Steps[0].Env = []ir.Var{
		{Name: "MODE", Value: ir.Value{Lit: "fast"}},
		{Name: "API_TOKEN", Value: ir.Value{Kind: ir.ValueSecret, Secret: "api-token"}},
	}
	files, _ := emit(t, project(j))
	task := dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])
	params := dict(dict(task["config"])["params"])
	if params["MODE"] != "fast" || params["API_TOKEN"] != "((api-token))" || params["DEPLOY"] != "((DEPLOY))" || params["region"] != "((region))" {
		t.Fatalf("params = %v", params)
	}
	args := list(dict(dict(task["config"])["run"])["args"])
	script := args[len(args)-1].(string)
	if !strings.Contains(script, "${DEPLOY}") || strings.Contains(script, "((") {
		t.Fatalf("script = %q", script)
	}
}

func TestDefaultShellFlagsMatchOtherEmitters(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Origin = ir.Origin{Provider: "tekton", Dialect: "tekton"}
	files, _ := emit(t, p)
	run := dict(dict(dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])["config"])["run"])
	if run["path"] != "sh" || list(run["args"])[0] != "-ec" {
		t.Fatalf("run = %v", run)
	}
}

func TestBashStepKeepsItsShell(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].Shell = "bash"
	files, _ := emit(t, project(j))
	run := dict(dict(dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])["config"])["run"])
	if run["path"] != "bash" {
		t.Fatalf("run = %v", run)
	}
}

func TestStepWorkdirIsUnderTheWorkspace(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].WorkDir = "app"
	files, _ := emit(t, project(j))
	run := dict(dict(dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])["config"])["run"])
	if run["dir"] != "workspace/app" {
		t.Fatalf("run = %v", run)
	}
	back, _ := reparse(t, files)
	if got := back.Pipelines[0].Jobs[0].Steps[0].WorkDir; got != "app" {
		t.Fatalf("workdir came back as %q", got)
	}
}

func TestStepTimeoutAndContinueOnError(t *testing.T) {
	j := scriptJob("build", "make")
	d := ir.Duration(90_000_000_000)
	j.Steps[0].Timeout, j.Steps[0].ContinueOnError = &d, true
	files, _ := emit(t, project(j))
	step := dict(list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])[1])
	inner := dict(step["try"])
	if inner["timeout"] != "1m30s" || inner["task"] != "build-run" {
		t.Fatalf("step = %v", step)
	}
	back, _ := reparse(t, files)
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if !st.ContinueOnError || st.Timeout == nil {
		t.Fatalf("parsed back %+v", st)
	}
}

func TestJobTimeoutAndRetryWrapThePlan(t *testing.T) {
	j := scriptJob("build", "make")
	d := ir.Duration(3_600_000_000_000)
	j.Timeout, j.Retry = &d, &ir.Retry{Attempts: 3}
	files, _ := emit(t, project(j))
	plan := list(jobDoc(t, pipelineDoc(t, files), "build")["plan"])
	if len(plan) != 1 || dict(plan[0])["attempts"] != 3 || dict(plan[0])["timeout"] != "1h0m0s" {
		t.Fatalf("plan = %v", plan)
	}
	back, _ := reparse(t, files)
	got := back.Pipelines[0].Jobs[0]
	if got.Retry == nil || got.Retry.Attempts != 3 || got.Timeout == nil {
		t.Fatalf("parsed back %+v", got)
	}
}

func TestHooksTakeOneStepAndSeveralWrapInDo(t *testing.T) {
	j := scriptJob("build", "make")
	fail := func(id string) *ir.Step {
		return &ir.Step{ID: id, Kind: ir.StepScript, Script: []string{"echo " + id}, If: callOf("failure")}
	}
	always := &ir.Step{ID: "tidy", Kind: ir.StepScript, Script: []string{"rm -rf tmp"}, If: callOf("always")}
	j.Steps = append(j.Steps, fail("notify"), always, fail("page"))
	files, _ := emit(t, project(j))
	jd := jobDoc(t, pipelineDoc(t, files), "build")
	if dict(jd["on_failure"])["do"] == nil || len(list(dict(jd["on_failure"])["do"])) != 2 {
		t.Fatalf("on_failure = %v", jd["on_failure"])
	}
	if dict(jd["ensure"])["task"] != "tidy" {
		t.Fatalf("ensure = %v", jd["ensure"])
	}
	back, _ := reparse(t, files)
	if n := len(back.Pipelines[0].Jobs[0].Steps); n != 4 {
		t.Fatalf("parsed back %d steps", n)
	}
}

func TestFinallyBecomesEnsureOnTheSinkJob(t *testing.T) {
	a, b := scriptJob("a", "true"), scriptJob("b", "true")
	b.Needs = []ir.Need{{JobID: "a"}}
	p := project(a, b)
	p.Pipelines[0].Finally = []*ir.Job{scriptJob("cleanup", "rm -rf x"), scriptJob("report", "echo done")}
	files, rep := emit(t, p)
	d := pipelineDoc(t, files)
	if dict(jobDoc(t, d, "a")["ensure"]) != nil {
		t.Fatal("a job with a dependant got the finally steps")
	}
	ens := dict(jobDoc(t, d, "b")["ensure"])
	if len(list(ens["do"])) != 2 {
		t.Fatalf("ensure = %v", ens)
	}
	if !hasFeatureEntry(rep, string(caps.PipelineFinally), "") {
		t.Fatalf("no finally entry")
	}
}

func TestFinallyWithNoSingleSinkIsLost(t *testing.T) {
	p := project(scriptJob("a", "true"), scriptJob("b", "true"))
	p.Pipelines[0].Finally = []*ir.Job{scriptJob("cleanup", "rm -rf x")}
	files, rep := emit(t, p)
	if strings.Contains(files[".concourse/ci.yml"], "ensure") || !hasFeatureEntry(rep, string(caps.PipelineFinally), "single sink") {
		t.Fatalf("finally must be lost: %v\n%s", rep.Entries, files[".concourse/ci.yml"])
	}
}

func TestStepGuardReadsTheGitFileAndParsesBack(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = eqRef("branch", "main")
	files, _ := emit(t, project(j))
	src := files[".concourse/ci.yml"]
	if !strings.Contains(src, "workspace/.git/branch") || !strings.Contains(src, "# pipemaster:guard") {
		t.Fatalf("no guard:\n%s", src)
	}
	back, _ := reparse(t, files)
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.If == nil || len(st.Script) != 1 || st.Script[0] != "make" {
		t.Fatalf("parsed back %+v", st)
	}
}

func TestStepGuardEditIsKept(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = eqRef("branch", "main")
	files, _ := emit(t, project(j))
	edited := strings.Replace(files[".concourse/ci.yml"], "then exit 0; fi # pipemaster:guard", "then exit 3; fi # pipemaster:guard", 1)
	if edited == files[".concourse/ci.yml"] {
		t.Fatalf("guard text changed shape:\n%s", files[".concourse/ci.yml"])
	}
	back, _ := parseFiles(t, map[string]string{"ci.yml": edited})
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.If != nil || !strings.Contains(strings.Join(st.Script, "\n"), "exit 3") {
		t.Fatalf("an edited guard was restored: %+v", st)
	}
}

func TestUnguardableStepConditionFailsSafe(t *testing.T) {
	j := scriptJob("build", "make")
	j.Steps[0].If = expr.Opaque{Dialect: "gha", Text: "always()"}
	files, rep := emit(t, project(j))
	if !strings.Contains(files[".concourse/ci.yml"], "exit 0") || !hasFeatureEntry(rep, string(caps.StepIf), "") {
		t.Fatalf("no fail-safe:\n%s", files[".concourse/ci.yml"])
	}
}

func TestUnguardableJobConditionLeavesNoTriggeringGet(t *testing.T) {
	j := scriptJob("build", "make")
	j.If = expr.Opaque{Dialect: "gha", Text: "github.event_name == 'push'"}
	p := project(j)
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main")}
	files, rep := emit(t, p)
	for _, s := range list(jobDoc(t, pipelineDoc(t, files), "build")["plan"]) {
		if dict(s)["trigger"] == true {
			t.Fatalf("an untranslatable condition runs unguarded: %v", s)
		}
	}
	if !hasFeatureEntry(rep, string(caps.JobIf), "") {
		t.Fatalf("no JobIf entry: %v", rep.Entries)
	}
}

const noGetJobFile = `resources:
  - name: src
    type: git
    source: {uri: https://x/y.git, branch: main}
jobs:
  - name: a
    plan:
      - get: src
        trigger: true
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo a"]}
  - name: b
    plan:
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo b"]}
`

// A need that an edit adds to a job whose file had no get must still order the jobs: the job gets the source
// with a passed list, and stays a job that only a person starts.
func TestNeedAddedToAJobOfAFileWithNoGetIsWritten(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": noGetJobFile})
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID == "b" {
			j.Needs = []ir.Need{{JobID: "a"}}
		}
	}
	files, _ := emit(t, p)
	get := dict(list(jobDoc(t, pipelineDoc(t, files), "b")["plan"])[0])
	passed := list(get["passed"])
	if get["get"] != "src" || len(passed) != 1 || passed[0] != "a" {
		t.Fatalf("first step of b = %v, want a get of src with passed [a]", get)
	}
	if get["trigger"] == true {
		t.Fatalf("b was a manual job, but its get triggers: %v", get)
	}
	back, _ := reparse(t, files)
	for _, j := range back.Pipelines[0].Jobs {
		if j.ID == "b" && (len(j.Needs) != 1 || j.Needs[0].JobID != "a" || !j.Manual) {
			t.Fatalf("b came back as needs %v manual %v", j.Needs, j.Manual)
		}
	}
}

const nestedGetFile = `resources:
  - name: src
    type: git
    source: {uri: https://x/y.git, branch: main}
jobs:
  - name: a
    plan:
      - get: src
        trigger: true
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo a"]}
  - name: b
    plan:
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo b"]}
      - in_parallel:
          - get: src
            passed: [a]
`

// A passed list nested in the plan already holds the needs, so the emitter writes no second get.
func TestNeedsHeldByANestedGetAddNoGet(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{"ci.yml": nestedGetFile})
	files, _ := emit(t, p)
	plan := list(jobDoc(t, pipelineDoc(t, files), "b")["plan"])
	if _, has := dict(plan[0])["get"]; has {
		t.Fatalf("first step of b = %v, want no added get", plan[0])
	}
}

const nestedGetThreeJobsFile = `resources:
  - name: src
    type: git
    source: {uri: https://x/y.git, branch: main}
jobs:
  - name: a
    plan:
      - get: src
        trigger: true
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo a"]}
  - name: c
    plan:
      - get: src
        trigger: true
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo c"]}
  - name: d
    plan:
      - get: src
        trigger: true
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo d"]}
  - name: b
    plan:
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo b"]}
      - in_parallel:
          - get: src
            passed: [a]
`

func needsOfAfterEdit(t *testing.T, added ...string) []string {
	t.Helper()
	p, _ := parseFiles(t, map[string]string{"ci.yml": nestedGetThreeJobsFile})
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID == "b" {
			for _, id := range added {
				j.Needs = append(j.Needs, ir.Need{JobID: id})
			}
		}
	}
	files, _ := emit(t, p)
	back, _ := reparse(t, files)
	var got []string
	for _, j := range back.Pipelines[0].Jobs {
		if j.ID == "b" {
			for _, n := range j.Needs {
				got = append(got, n.JobID)
			}
		}
	}
	slices.Sort(got)
	return got
}

// A need that is not in the nested passed list must still be written, whether one or several are new.
func TestNeedAddedBesideANestedGetIsKept(t *testing.T) {
	for _, tc := range []struct {
		want  []string
		added []string
	}{
		{want: []string{"a"}},
		{added: []string{"c"}, want: []string{"a", "c"}},
		{added: []string{"c", "d"}, want: []string{"a", "c", "d"}},
	} {
		if got := needsOfAfterEdit(t, tc.added...); !slices.Equal(got, tc.want) {
			t.Fatalf("added %v: b needs %v, want %v", tc.added, got, tc.want)
		}
	}
}

// The args of a task drop one trailing newline and split at a newline, so a script that has either must come back
// from the carry marker unchanged, and the report must say so.
func TestScriptTheArgsCannotSayComesBackExact(t *testing.T) {
	for _, script := range [][]string{
		{"echo two", ""},
		{"echo a", "", ""},
		{"echo a\necho b"},
		{"echo a", "echo b\nsecond", ""},
	} {
		files, rep := emit(t, project(scriptJob("a", script...)))
		back, _ := reparse(t, files)
		if got := back.Pipelines[0].Jobs[0].Steps[0].Script; !slices.Equal(got, script) {
			t.Fatalf("script %q came back as %q", script, got)
		}
		if !hasFeatureEntry(rep, string(caps.StepScript), "carry marker holds the script") {
			t.Fatalf("no entry for script %q: %v", script, rep.Entries)
		}
	}
}

const needTaskStep = `
      - task: t
        config:
          platform: linux
          image_resource: {type: registry-image, source: {repository: alpine}}
          run: {path: sh, args: [-c, "echo x"]}`

// needPlacementFile has the jobs a, c1 (gets src), c2 (gets other) and c3 (gets nothing), and a job b whose plan is
// the given steps.
func needPlacementFile(bPlan string) string {
	return `resources:
  - name: src
    type: git
    source: {uri: https://x/y.git, branch: main}
  - name: other
    type: git
    source: {uri: https://x/z.git, branch: main}
jobs:
  - name: a
    plan:
      - get: src
        trigger: true` + needTaskStep + `
  - name: c1
    plan:
      - get: src
        trigger: true` + needTaskStep + `
  - name: c2
    plan:
      - get: other
        trigger: true` + needTaskStep + `
  - name: c3
    plan:` + needTaskStep + `
  - name: b
    plan:` + bPlan + `
`
}

const (
	nestedPlan = needTaskStep + `
      - in_parallel:
          - get: src
            passed: [a]`
	noGetPlan = needTaskStep
)

// Whatever need is added, the file must pass fly --strict, and a need that no passed list can write is reported.
func TestAddedNeedIsWrittenWhereFlyAcceptsIt(t *testing.T) {
	for _, tc := range []struct {
		name, plan, need string
		want             []string
		lost             bool
	}{
		{name: "nested get, shared resource", plan: nestedPlan, need: "c1", want: []string{"a", "c1"}},
		{name: "nested get, other resource", plan: nestedPlan, need: "c2", want: []string{"a", "c2"}},
		{name: "nested get, no resource", plan: nestedPlan, need: "c3", want: []string{"a"}, lost: true},
		{name: "no get, shared resource", plan: noGetPlan, need: "c1", want: []string{"c1"}},
		{name: "no get, other resource", plan: noGetPlan, need: "c2", want: []string{"c2"}},
		{name: "no get, no resource", plan: noGetPlan, need: "c3", lost: true},
	} {
		p, _ := parseFiles(t, map[string]string{"ci.yml": needPlacementFile(tc.plan)})
		for _, j := range p.Pipelines[0].Jobs {
			if j.ID == "b" {
				j.Needs = append(j.Needs, ir.Need{JobID: tc.need})
			}
		}
		files, rep := emit(t, p)
		back, _ := reparse(t, files)
		var got []string
		for _, j := range back.Pipelines[0].Jobs {
			if j.ID == "b" {
				for _, n := range j.Needs {
					got = append(got, n.JobID)
				}
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: b needs %v, want %v", tc.name, got, tc.want)
		}
		if lost := hasFeatureEntry(rep, string(caps.JobNeeds), "need is not written"); lost != tc.lost {
			t.Errorf("%s: lost entry = %v, want %v", tc.name, lost, tc.lost)
		}
	}
}

// A script edited to an empty last line or a newline inside a line cannot be written in the args of a task of a
// Concourse file, so the report must say that the parser reads it changed.
func TestEditedScriptOfAConcourseFileIsReportedWhenTheArgsCannotSayIt(t *testing.T) {
	for _, script := range [][]string{{"echo x", ""}, {"echo a\necho b"}} {
		p, _ := parseFiles(t, map[string]string{"ci.yml": needPlacementFile(noGetPlan)})
		p.Pipelines[0].Jobs[0].Steps[0].Script = script
		_, rep := emit(t, p)
		if !hasFeatureEntry(rep, string(caps.StepScript), "parser reads the script trimmed or split") {
			t.Fatalf("no entry for script %q: %v", script, rep.Entries)
		}
	}
}

// A need must be written or reported whichever step kind nests the get that holds the passed list.
func TestNeedBesideAGetNestedInAHookIsNotDropped(t *testing.T) {
	plan := needTaskStep + `
    on_success:
      in_parallel:
        - get: src
          passed: [a]`
	p, _ := parseFiles(t, map[string]string{"ci.yml": needPlacementFile(plan)})
	for _, j := range p.Pipelines[0].Jobs {
		if j.ID == "b" {
			j.Needs = append(j.Needs, ir.Need{JobID: "c1"})
		}
	}
	files, rep := emit(t, p)
	back, _ := reparse(t, files)
	var got []string
	for _, j := range back.Pipelines[0].Jobs {
		if j.ID == "b" {
			for _, n := range j.Needs {
				got = append(got, n.JobID)
			}
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"a", "c1"}) && !hasFeatureEntry(rep, string(caps.JobNeeds), "not written") {
		t.Fatalf("b needs %v with no entry: %v", got, rep.Entries)
	}
}

func workspaceSource(t *testing.T, files map[string]string, job string) string {
	t.Helper()
	plan := list(jobDoc(t, pipelineDoc(t, files), job)["plan"])
	for _, s := range plan {
		if im := dict(dict(s)["input_mapping"]); im != nil {
			src, _ := im["workspace"].(string)
			return src
		}
	}
	t.Fatalf("no task maps the workspace in %v", plan)
	return ""
}

func TestTagOnlyJobBuildsTheTag(t *testing.T) {
	p := project(scriptJob("publish", "make publish"))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}}}
	files, rep := emit(t, p)
	if got := workspaceSource(t, files, "publish"); got != "source-tags" {
		t.Fatalf("the workspace comes from %q, want the tag resource", got)
	}
	if hasEntry(rep, "newest version of each resource") {
		t.Fatalf("one triggering resource needs no multi-resource entry: %v", rep.Entries)
	}
}

func TestTwoTriggeringResourcesUseTheFirstAndSayIt(t *testing.T) {
	p := project(scriptJob("build", "make"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main", "release")}
	files, rep := emit(t, p)
	if got := workspaceSource(t, files, "build"); got != "source-main" {
		t.Fatalf("the workspace comes from %q, want the first resource", got)
	}
	if !hasFeatureEntry(rep, "trigger.push", "source-main") || !hasFeatureEntry(rep, "trigger.push", "newest version of each resource") {
		t.Fatalf("no entry names the workspace resource and the newest-version rule: %v", rep.Entries)
	}
}

func TestBranchAndTagTriggersSayWhichResourceTheWorkspaceFollows(t *testing.T) {
	p := project(scriptJob("publish", "make publish"))
	p.Pipelines[0].Triggers = []ir.Trigger{pushOn("main"), {Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}}}}
	files, rep := emit(t, p)
	if got := workspaceSource(t, files, "publish"); got != "source" {
		t.Fatalf("the workspace comes from %q", got)
	}
	if !hasFeatureEntry(rep, "trigger.push", "a push to source-tags") {
		t.Fatalf("no entry says that a tag push builds the first resource: %v", rep.Entries)
	}
}
