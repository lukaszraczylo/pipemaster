package gitlab_test

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

func levelAt(rep *report.Report, f caps.Feature, path string) (caps.Level, bool) {
	found := false
	lvl := caps.Native
	for _, e := range rep.Entries {
		if e.Feature == f && e.IRPath == path {
			found = true
			lvl = max(lvl, e.Level)
		}
	}
	return lvl, found
}

func schemaOK(t *testing.T, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		if err := validate.File("gitlab", name, data); err != nil {
			t.Errorf("schema: %v\n%s", err, data)
		}
	}
}

func TestTemplatedActionValuesDoNotLeak(t *testing.T) {
	up := ghaUses("up", "upload-artifact", map[string]string{"name": "coverage-${{ matrix.os }}", "path": "out"})
	goSetup := ghaUses("go", "setup-go", map[string]string{"go-version": "${{ matrix.go }}"})
	j := glJob("build", goSetup, glStep("make"), up)
	j.Matrix = &ir.Matrix{
		Axes:    []ir.Axis{{Name: "os", Values: []string{"a", "b"}}, {Name: "go", Values: []string{"1.24"}}},
		Exclude: []ir.Combo{{{Key: "os", Value: "${{ github.sha }}"}}},
	}
	files, _ := emitFiles(t, glProject("gha", j))
	doc := string(files[rootFile])
	if strings.Contains(doc, "${{") {
		t.Fatalf("raw expression leaked:\n%s", doc)
	}
	if want := "name: coverage-$os"; !strings.Contains(doc, want) {
		t.Errorf("want %q in\n%s", want, doc)
	}
	_, rep := emitFiles(t, glProject("gha", j))
	if lvl, _ := levelAt(rep, caps.StepAction, "pipeline/gitlab-ci/job/build/step/go"); lvl != caps.Lost {
		t.Errorf("a setup-go version that is an expression is lost by the actions table: %v", lvl)
	}
	schemaOK(t, files)
}

func TestInputsDescriptionDoesNotLeak(t *testing.T) {
	p := glProject("gha", glJob("build", glStep("make")))
	p.Pipelines[0].Inputs = []ir.Input{{Name: "target", Default: "prod", Description: "deploy ${{ github.sha }}"}}
	if doc := emitDoc(t, p); strings.Contains(doc, "${{") {
		t.Fatalf("raw expression leaked:\n%s", doc)
	}
}

func TestEmitFailsLoudlyOnRawExpression(t *testing.T) {
	p := glProject("gitlab", glJob("build", glStep("make")))
	p.Pipelines[0].Ext = ir.Ext{"gitlab": map[string]any{
		"hidden": map[string]any{".tpl": map[string]any{"script": []any{"echo ${{ x }}"}}},
	}}
	_, _, err := gitlab.New().Emit(p)
	if err == nil || !strings.Contains(err.Error(), "${{") {
		t.Fatalf("a raw ${{ in the output must be an error, got %v", err)
	}
}

func TestActionStepFieldsAreAppliedOrLost(t *testing.T) {
	branch := eq(ref(expr.CtxBranch), strLit("main"))
	decorate := func(st *ir.Step) *ir.Step {
		st.If = branch
		st.Env = []ir.Var{{Name: "A", Value: ir.Value{Lit: "1"}}}
		st.WorkDir = "w"
		st.ContinueOnError = true
		st.Timeout = durOf(5)
		st.Shell = "bash"
		return st
	}
	cache := decorate(ghaUses("c", "cache", map[string]string{"path": "x", "key": "k"}))
	setup := decorate(ghaUses("g", "setup-go", map[string]string{"go-version": "1.24"}))
	files, rep := emitFiles(t, glProject("gha", glJob("build", cache, setup, glStep("make"))))
	for _, id := range []string{"c", "g"} {
		path := "pipeline/gitlab-ci/job/build/step/" + id
		for _, f := range []caps.Feature{caps.StepIf, caps.StepEnv, caps.StepWorkdir, caps.StepContinueOnError, caps.StepTimeout} {
			lvl, ok := levelAt(rep, f, path)
			if !ok || lvl != caps.Lost {
				t.Errorf("%s at %s = %v (found %v), want lost: the keyword job form cannot carry it", f, path, lvl, ok)
			}
		}
	}
	schemaOK(t, files)
}

func TestUploadArtifactStatusBecomesArtifactsWhen(t *testing.T) {
	cases := map[string]struct {
		cond expr.Expr
		want string
		lost bool
	}{
		"always":  {cond: expr.Call{Name: "always"}, want: "when: always"},
		"failure": {cond: expr.Call{Name: "failure"}, want: "when: on_failure"},
		"branch":  {cond: eq(ref(expr.CtxBranch), strLit("main")), lost: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			up := ghaUses("up", "upload-artifact", map[string]string{"name": "bin", "path": "out"})
			up.If = c.cond
			files, rep := emitFiles(t, glProject("gha", glJob("build", glStep("make"), up)))
			doc := string(files[rootFile])
			lvl, _ := levelAt(rep, caps.StepIf, "pipeline/gitlab-ci/job/build/step/up")
			if c.lost {
				if lvl != caps.Lost {
					t.Errorf("an arbitrary condition cannot guard artifacts: %v", lvl)
				}
				return
			}
			if !strings.Contains(doc, c.want) || lvl == caps.Lost {
				t.Errorf("want %q and a non-lost step.if (got %v):\n%s", c.want, lvl, doc)
			}
			schemaOK(t, files)
		})
	}
}

func TestAlwaysStepMovesToAfterScript(t *testing.T) {
	cleanup := &ir.Step{ID: "cleanup", Kind: ir.StepScript, Script: []string{"rm -rf tmp"}, If: expr.Call{Name: "always"}}
	p := glProject("gitlab", glJob("build", glStep("make"), cleanup))
	back, files := roundTrip(t, p)
	doc := string(files[rootFile])
	if !strings.Contains(doc, "# pipemaster:emulated step.if pipeline/gitlab-ci/job/build/step/cleanup") {
		t.Errorf("the move needs a marker:\n%s", doc)
	}
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("the marker must collapse:\n%s\n%s", diff, doc)
	}
}

func TestNeedlessFinallyJobGetsPostStage(t *testing.T) {
	build := glJob("build", glStep("make"))
	build.Ext = ir.Ext{"gitlab": map[string]any{"stage": "test"}}
	ship := glJob("ship", glStep("make ship"))
	ship.Needs = []ir.Need{{JobID: "build"}}
	ship.Ext = ir.Ext{"gitlab": map[string]any{"stage": "deploy"}}
	fin := glJob("cleanup", glStep("rm -rf tmp"))
	fin.If = expr.Call{Name: "always"}
	p := glProject("gitlab", build, ship)
	p.Pipelines[0].Finally = []*ir.Job{fin}
	p.Pipelines[0].Ext = ir.Ext{"gitlab": map[string]any{"stages": []any{"test", "deploy"}}}
	doc := emitDoc(t, p)
	if !strings.Contains(doc, "stage: .post") {
		t.Errorf("a finally job without needs must run after every stage:\n%s", doc)
	}
	assertStagesRespectNeeds(t, doc)
}

func TestForeignExtIsReportedLost(t *testing.T) {
	st := glStep("make")
	st.Ext["gha"] = map[string]any{"id": "x"}
	j := glJob("build", st)
	j.Ext = ir.Ext{"gha": map[string]any{"x": "y"}}
	p := glProject("gha", j)
	p.Pipelines[0].Ext = ir.Ext{"gha": map[string]any{"on": "push"}}
	_, rep := emitFiles(t, p)
	for _, path := range []string{"pipeline/gitlab-ci", glJobPath, glJobPath + "/step/script"} {
		lvl, ok := levelAt(rep, caps.ExtPassthrough, path)
		if !ok || lvl != caps.Lost {
			t.Errorf("foreign Ext at %s = %v (found %v), want lost", path, lvl, ok)
		}
	}
	found := false
	for _, e := range rep.Entries {
		found = found || (e.Feature == caps.ExtPassthrough && strings.Contains(e.Detail, "round-trip metadata"))
	}
	if !found {
		t.Errorf("detail must say the data travels in round-trip metadata: %+v", rep.Entries)
	}
}

func TestSingleElementRefDoesNotPanic(t *testing.T) {
	for _, name := range []string{"env", "gitlab", "secrets", "needs", "matrix"} {
		files, _ := emitFiles(t, glProject("gha", glJob("build", glStep("echo ${{ "+name+" }} done", "echo ok"))))
		if doc := string(files[rootFile]); !strings.Contains(doc, "echo ok") || strings.Contains(doc, "${{") {
			t.Errorf("%s: want the bad line commented out and the rest kept:\n%s", name, doc)
		}
	}
}

func TestReservedJobIDsAreRenamed(t *testing.T) {
	var jobs []*ir.Job
	for _, id := range []string{"cache", "default", "image", "services", "variables", "stages", "include", "workflow", "before_script", "after_script", "pages", ".hidden", "true"} {
		jobs = append(jobs, glJob(id, glStep("make "+id)))
	}
	deploy := glJob("deploy", glStep("make deploy"))
	deploy.Needs = []ir.Need{{JobID: "cache"}}
	p := glProject("gitlab", append(jobs, deploy)...)
	back, files := roundTrip(t, p)
	doc := string(files[rootFile])
	if strings.Contains(doc, "\ncache:\n  stage") || strings.Contains(doc, "\ncache:\n  script") {
		t.Errorf("a job must not use a global keyword as its key:\n%s", doc)
	}
	got := map[string]string{}
	for _, j := range back.Pipelines[0].Jobs {
		got[j.ID] = jobNeeds(j)
	}
	for _, j := range p.Pipelines[0].Jobs {
		if w, ok := got[j.ID]; !ok || w != jobNeeds(j) {
			t.Errorf("job %q must come back with needs %q, got %q (present %v)\n%s", j.ID, jobNeeds(j), w, ok, doc)
		}
	}
	_, rep := emitFiles(t, p)
	if lvl, ok := levelAt(rep, caps.ExtPassthrough, "pipeline/gitlab-ci/job/cache"); !ok || lvl != caps.Emulated {
		t.Errorf("the rename is an emulated entry: %v %v", lvl, ok)
	}
	schemaOK(t, files)
}

func TestCacheKeyWithRunnerOSPrefix(t *testing.T) {
	key := "${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}"
	build := glJob("build", ghaUses("cache", "cache", map[string]string{"path": "x", "key": key}))
	files, rep := emitFiles(t, glProject("gha", build))
	doc := string(files[rootFile])
	for _, want := range []string{"files:", "**/go.sum", "prefix: go"} {
		if !strings.Contains(doc, want) {
			t.Errorf("want %q in\n%s", want, doc)
		}
	}
	found := false
	for _, e := range rep.Entries {
		found = found || (e.Feature == caps.StepAction && e.Level == caps.Emulated && strings.Contains(e.Detail, "runner.os"))
	}
	if !found {
		t.Errorf("dropping runner.os is an emulated entry naming it: %+v", rep.Entries)
	}
	schemaOK(t, files)
}

func twoWorkflows() *ir.Project {
	ci := &ir.Pipeline{
		ID: "ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}}},
		Jobs: []*ir.Job{glJob("build", glStep("make")), glJob("test", glStep("make test"))},
	}
	ci.Jobs[1].Needs = []ir.Need{{JobID: "build"}}
	rel := &ir.Pipeline{
		ID: "release", Triggers: []ir.Trigger{{Kind: ir.TriggerTag}},
		Jobs:    []*ir.Job{glJob("build", glStep("make release"))},
		Finally: []*ir.Job{glJob("notify", glStep("./notify.sh"))},
	}
	rel.Finally[0].If = expr.Call{Name: "always"}
	return &ir.Project{Origin: ir.Origin{Provider: "gha", Dialect: "gha"}, Pipelines: []*ir.Pipeline{ci, rel}}
}

func TestMultiPipelineSharesOneFile(t *testing.T) {
	p := twoWorkflows()
	files, rep := emitFiles(t, p)
	if len(files) != 1 {
		t.Fatalf("one file expected, got %d", len(files))
	}
	doc := string(files[rootFile])
	for _, want := range []string{"ci:build:", "release:build:", "workflow:"} {
		if !strings.Contains(doc, want) {
			t.Errorf("want %q in\n%s", want, doc)
		}
	}
	schemaOK(t, files)
	assertStagesRespectNeeds(t, doc)
	n := 0
	for _, e := range rep.Entries {
		if e.Feature == caps.ProjectMultiPipeline {
			n++
			if e.Level != caps.Emulated || e.IRPath != "pipeline/release" {
				t.Errorf("multi-pipeline entry %+v, want emulated at pipeline/release", e)
			}
		}
	}
	if n != 1 {
		t.Errorf("project.multi_pipeline must be reported once, got %d", n)
	}
	back, _, err := gitlabParse(files)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, doc)
	}
	if len(back.Pipelines) != len(p.Pipelines) {
		t.Fatalf("pipelines: got %d want %d\n%s", len(back.Pipelines), len(p.Pipelines), doc)
	}
	for i, want := range p.Pipelines {
		got := back.Pipelines[i]
		if got.ID != want.ID || jobIDs(got.Jobs) != jobIDs(want.Jobs) || jobIDs(got.Finally) != jobIDs(want.Finally) {
			t.Errorf("pipeline %d: got %s %s/%s want %s %s/%s", i, got.ID, jobIDs(got.Jobs), jobIDs(got.Finally), want.ID, jobIDs(want.Jobs), jobIDs(want.Finally))
		}
		if len(got.Triggers) != len(want.Triggers) || got.Triggers[0].Kind != want.Triggers[0].Kind {
			t.Errorf("pipeline %s triggers: got %+v want %+v", want.ID, got.Triggers, want.Triggers)
		}
		for _, j := range got.Jobs {
			if j.If != nil {
				t.Errorf("job %s/%s keeps the pipeline trigger condition: %s", got.ID, j.ID, expr.String(j.If))
			}
		}
	}
}

func jobNeeds(j *ir.Job) string {
	var ids []string
	for _, n := range j.Needs {
		ids = append(ids, n.JobID)
	}
	return strings.Join(ids, ",")
}

func jobIDs(js []*ir.Job) string {
	var ids []string
	for _, j := range js {
		ids = append(ids, j.ID)
	}
	return strings.Join(ids, ",")
}

func TestMultiPipelineJobsCarryTriggerCondition(t *testing.T) {
	doc := emitDoc(t, twoWorkflows())
	if !strings.Contains(doc, `$CI_COMMIT_BRANCH == "main"`) || !strings.Contains(doc, "$CI_COMMIT_TAG") {
		t.Errorf("each job must carry its pipeline's trigger condition:\n%s", doc)
	}
}

func TestSinglePipelineKeepsPlainJobIDs(t *testing.T) {
	doc := emitDoc(t, glProject("gha", glJob("build", glStep("make"))))
	if strings.Contains(doc, "project.multi_pipeline") || strings.Contains(doc, "gitlab-ci:build") {
		t.Errorf("one pipeline needs no prefix and no marker:\n%s", doc)
	}
}

func rootMap(t *testing.T, doc string) map[string]any {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func firstRule(t *testing.T, root map[string]any, job string) map[string]any {
	t.Helper()
	jm, _ := root[job].(map[string]any)
	rules, _ := jm["rules"].([]any)
	if len(rules) == 0 {
		t.Fatalf("job %s has no rules: %v", job, root[job])
	}
	r, _ := rules[0].(map[string]any)
	return r
}

func TestMultiPipelineJobKeepsItsPathFilter(t *testing.T) {
	p := twoWorkflows()
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Paths: ir.Filter{Include: []string{"src/**"}}}}
	p.Pipelines[1].Triggers = []ir.Trigger{{Kind: ir.TriggerPush}}
	files, _ := emitFiles(t, p)
	root := rootMap(t, string(files[rootFile]))
	if ch, _ := firstRule(t, root, "ci:build")["changes"].([]any); len(ch) != 1 || ch[0] != "src/**" {
		t.Errorf("ci:build must keep changes: [src/**], rules %v", root["ci:build"])
	}
	if _, has := firstRule(t, root, "release:build")["changes"]; has {
		t.Errorf("release:build has no path filter")
	}
	schemaOK(t, files)
}

func TestProjectMultiPipelineCapsMatchLedger(t *testing.T) {
	want := gitlab.New().Caps()[caps.ProjectMultiPipeline].Level
	if want != caps.Emulated {
		t.Fatalf("Caps() says %v, the emitter emulates it", want)
	}
	_, rep := emitFiles(t, twoWorkflows())
	for _, e := range rep.Entries {
		if e.Feature == caps.ProjectMultiPipeline && e.Level != want {
			t.Errorf("ledger %v differs from Caps() %v", e.Level, want)
		}
	}
}

func TestFoldedOpaqueConditionSurvives(t *testing.T) {
	p := gitlabFixture(t, "expr.opaque")
	first := p.Pipelines[0]
	first.ID = "ci"
	second := &ir.Pipeline{ID: "rel", Triggers: first.Triggers}
	for _, j := range first.Jobs {
		cp := *j
		second.Jobs = append(second.Jobs, &cp)
	}
	p.Pipelines = append(p.Pipelines, second)
	var want []expr.Expr
	for _, j := range first.Jobs {
		want = append(want, j.If)
	}
	files, _ := emitFiles(t, p)
	back, _, err := gitlabParse(files)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, files[rootFile])
	}
	for _, pl := range back.Pipelines {
		for i, j := range pl.Jobs {
			if j.If == nil || expr.String(j.If) != expr.String(want[i]) {
				t.Errorf("%s/%s lost its opaque condition: %v", pl.ID, j.ID, j.If)
			}
		}
	}
}

func TestSuccessStatusIsAccounted(t *testing.T) {
	up := ghaUses("up", "upload-artifact", map[string]string{"name": "bin", "path": "out"})
	up.If = expr.Call{Name: "success"}
	st := glStep("make")
	st.If = expr.Call{Name: "success"}
	if _, _, err := gitlab.New().Emit(glProject("gha", glJob("build", st, up))); err != nil {
		t.Fatalf("success() is the default and must be accounted: %v", err)
	}
}

func TestFoldedPipelineWithoutGitLabTriggerIsDisabled(t *testing.T) {
	p := twoWorkflows()
	p.Pipelines[1].Triggers = []ir.Trigger{{Kind: ir.TriggerPipelineDone}}
	files, rep := emitFiles(t, p)
	root := rootMap(t, string(files[rootFile]))
	if r := firstRule(t, root, "release:build"); r["when"] != "never" {
		t.Errorf("a pipeline with no expressible trigger must not run: %v", r)
	}
	found := false
	for _, e := range rep.Entries {
		found = found || (e.Level == caps.Lost && strings.Contains(e.Detail, "release") && strings.Contains(e.IRPath, "release/job/build"))
	}
	if !found {
		t.Errorf("want a lost entry naming the pipeline: %+v", rep.Entries)
	}
}

func TestPagesJobRenamedOnlyForForeignOrigin(t *testing.T) {
	for origin, wantRename := range map[string]bool{"gitlab": false, "gha": true} {
		doc := emitDoc(t, glProject(origin, glJob("pages", glStep("make"))))
		renamed := strings.Contains(doc, "pages-job:")
		if renamed != wantRename {
			t.Errorf("origin %s: renamed=%v want %v\n%s", origin, renamed, wantRename, doc)
		}
	}
}

func TestMarkersCollapseForAnyPipelineID(t *testing.T) {
	for name, c := range emulCases() {
		t.Run(name, func(t *testing.T) {
			p := c.build()
			p.Pipelines[0].ID = "ci"
			files, _ := emitFiles(t, p)
			back, rep, err := gitlabParse(files)
			if err != nil {
				t.Fatalf("reparse: %v\n%s", err, files[rootFile])
			}
			for _, e := range rep.Entries {
				if strings.Contains(e.Detail, "marker not collapsed") {
					t.Errorf("uncollapsed marker: %+v\n%s", e, files[rootFile])
				}
			}
			back.Pipelines[0].ID = "ci"
			if ok, diff := ir.Equal(p, back); !ok {
				t.Errorf("features must come back under any pipeline ID:\n%s\n%s", diff, files[rootFile])
			}
		})
	}
}

func TestFoldedOpaqueRulesLoseTriggerConditionLoudly(t *testing.T) {
	p := gitlabFixture(t, "expr.opaque")
	first := p.Pipelines[0]
	first.ID = "ci"
	first.Triggers = []ir.Trigger{{Kind: ir.TriggerPush, Paths: ir.Filter{Include: []string{"src/**"}}}}
	second := &ir.Pipeline{ID: "rel", Triggers: []ir.Trigger{{Kind: ir.TriggerTag}}}
	cp := *first.Jobs[0]
	cp.ID = "other"
	second.Jobs = append(second.Jobs, &cp)
	p.Pipelines = append(p.Pipelines, second)
	_, rep := emitFiles(t, p)
	found := false
	for _, e := range rep.Entries {
		found = found || (e.Level == caps.Lost && strings.HasPrefix(e.IRPath, "pipeline/ci/job/") &&
			strings.Contains(e.Detail, "no longer include") && strings.Contains(e.Detail, "trigger condition"))
	}
	if !found {
		t.Errorf("an opaque rules job loses its pipeline's trigger condition and path filter; the ledger must say so: %+v", rep.Entries)
	}
}

func TestDuplicateMarkersDoNotMultiplyArguments(t *testing.T) {
	c := emulCases()["job.matrix"]
	p := c.build()
	files, _ := emitFiles(t, p)
	lines := strings.Split(string(files[rootFile]), "\n")
	var out []string
	for i, l := range lines {
		out = append(out, l)
		if strings.HasPrefix(l, "# pipemaster:emulated job.matrix") && i+1 < len(lines) {
			out = append(out, l, lines[i+1])
		}
	}
	files[rootFile] = []byte(strings.Join(out, "\n"))
	back, _, err := gitlabParse(files)
	if err != nil {
		t.Fatal(err)
	}
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("a repeated marker must collapse like one marker:\n%s", diff)
	}
}

func TestCapsDeclareDisplayNameLost(t *testing.T) {
	s := newProvider().(interface{ Caps() caps.Matrix }).Caps()[caps.DisplayName]
	if s.Level != caps.Lost || s.Caveat == "" {
		t.Fatalf("display.name caps = %+v, want lost with a caveat", s)
	}
	j := glJob("build", glStep("make"))
	j.Name = "build"
	p := &ir.Project{Pipelines: []*ir.Pipeline{{ID: "ci", Name: "CI", Jobs: []*ir.Job{j}}}}
	_, rep := emitFiles(t, p)
	got := map[string]bool{}
	for _, e := range rep.Entries {
		if e.Feature == caps.DisplayName {
			got[e.IRPath] = e.Level == caps.Lost
		}
	}
	if !got["pipeline/ci"] {
		t.Errorf("pipeline name must be reported lost: %+v", rep.Entries)
	}
	if lost, ok := got["pipeline/ci/job/build"]; ok && !lost {
		t.Errorf("a reported job name must be lost, got %+v", got)
	}
}

func TestResultWriteLineIsNotWritten(t *testing.T) {
	j := glJob("a", glStep("echo v > $(results.tag.path)", "make"))
	j.Outputs = []ir.Output{{Name: "tag"}}
	files, rep := emitFiles(t, glProject("tekton", j))
	for _, l := range strings.Split(string(files[rootFile]), "\n") {
		if strings.Contains(l, "results.") && !strings.HasPrefix(strings.TrimSpace(l), "#") {
			t.Fatalf("the result write reached the script:\n%s", files[rootFile])
		}
	}
	if lvl, ok := levelAt(rep, caps.JobOutputs, "pipeline/gitlab-ci/job/a"); !ok || lvl != caps.Lost {
		t.Fatalf("job.outputs level %v found %v: %+v", lvl, ok, rep.Entries)
	}
}

func TestOutputWithoutValueIsLost(t *testing.T) {
	j := glJob("a", glStep("make"))
	j.Outputs = []ir.Output{{Name: "tag"}}
	doc := emitDoc(t, glProject("tekton", j))
	if strings.Contains(doc, `"tag="`) || strings.Contains(doc, "outputs.env") {
		t.Fatalf("an output with no value was written:\n%s", doc)
	}
}
