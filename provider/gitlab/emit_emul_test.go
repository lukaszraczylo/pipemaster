package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const glJobPath = "pipeline/gitlab-ci/job/build"

func glProject(origin string, jobs ...*ir.Job) *ir.Project {
	return &ir.Project{
		Origin: ir.Origin{Provider: origin, Dialect: origin},
		Pipelines: []*ir.Pipeline{{
			ID: "gitlab-ci", Triggers: []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}, Jobs: jobs,
		}},
	}
}

func glStep(lines ...string) *ir.Step {
	return &ir.Step{ID: "script", Kind: ir.StepScript, Script: lines, Ext: ir.Ext{"gitlab": map[string]any{"section": "script"}}}
}

func glJob(id string, steps ...*ir.Step) *ir.Job { return &ir.Job{ID: id, Steps: steps} }

func ref(name string) expr.Expr { return expr.Ref{Path: []string{name}} }

func strLit(s string) expr.Expr { return expr.Lit{Kind: expr.LitStr, Str: s} }

func eq(l, r expr.Expr) expr.Expr { return expr.Binary{Op: expr.OpEq, L: l, R: r} }

func stepPathOf(job string) string { return "pipeline/gitlab-ci/job/" + job + "/step/script" }

// emulCase is one emulation: the IR that needs it, the text it must emit and whether Parse(Emit(ir)) restores the IR.
type emulCase struct {
	build   func() *ir.Project
	feature caps.Feature
	marker  string
	want    []string
	level   caps.Level
}

func emulCases() map[string]emulCase {
	stepIf := func() *ir.Project {
		st := glStep("make test")
		st.If = eq(ref(expr.CtxBranch), strLit("main"))
		return glProject("gitlab", glJob("build", st))
	}
	continueOnError := func() *ir.Project {
		st := glStep("make test", "make lint")
		st.ContinueOnError = true
		return glProject("gitlab", glJob("build", st))
	}
	env := func() *ir.Project {
		st := glStep("make test")
		st.Env = []ir.Var{{Name: "A", Value: ir.Value{Lit: "one"}}, {Name: "SHA", Value: ir.Value{Kind: ir.ValueExpr, Expr: expr.Ref{Path: []string{expr.CtxSHA}}}}}
		return glProject("gitlab", glJob("build", st))
	}
	workdir := func() *ir.Project {
		st := glStep("make test")
		st.WorkDir = "sub/dir"
		return glProject("gitlab", glJob("build", st))
	}
	all := func() *ir.Project {
		st := glStep("make test")
		st.If = eq(ref(expr.CtxBranch), strLit("main"))
		st.ContinueOnError = true
		st.Env = []ir.Var{{Name: "A", Value: ir.Value{Lit: "one"}}}
		st.WorkDir = "sub"
		return glProject("gitlab", glJob("build", st))
	}
	outputs := func() *ir.Project {
		j := glJob("build", glStep("make"))
		j.Outputs = []ir.Output{{Name: "ver", Value: "v1-${{ sha }}"}}
		return glProject("gitlab", j)
	}
	matrix := func() *ir.Project {
		j := glJob("build", glStep("make"))
		j.Matrix = &ir.Matrix{
			Axes:    []ir.Axis{{Name: "os", Values: []string{"a", "b"}}, {Name: "v", Values: []string{"1", "2"}}},
			Exclude: []ir.Combo{{{Key: "os", Value: "a"}, {Key: "v", Value: "1"}}},
			Include: []ir.Combo{{{Key: "os", Value: "c"}, {Key: "v", Value: "9"}}},
		}
		return glProject("gitlab", j)
	}
	concurrency := func() *ir.Project {
		p := glProject("gitlab", glJob("build", glStep("make")), glJob("test", glStep("make test")))
		p.Pipelines[0].Concurrency = &ir.Concurrency{Group: "deploy"}
		return p
	}
	inputs := func() *ir.Project {
		p := glProject("gitlab", glJob("build", glStep("make")))
		p.Pipelines[0].Inputs = []ir.Input{{Name: "target", Default: "prod", Description: "where to deploy"}}
		return p
	}
	alwaysStep := func() *ir.Project {
		cleanup := &ir.Step{ID: "cleanup", Kind: ir.StepScript, Script: []string{"rm -rf tmp"}, If: expr.Call{Name: "always"}}
		return glProject("gitlab", glJob("build", glStep("make"), cleanup))
	}
	return map[string]emulCase{
		"step.if.always": {
			build: alwaysStep, feature: caps.StepIf, marker: "# pipemaster:emulated step.if pipeline/gitlab-ci/job/build/step/cleanup", level: caps.Emulated,
			want: []string{"after_script:", "rm -rf tmp"},
		},
		"step.if": {
			build: stepIf, feature: caps.StepIf, marker: "# pipemaster:emulated step.if " + stepPathOf("build"), level: caps.Emulated,
			want: []string{`if [ "$CI_COMMIT_BRANCH" = "main" ]; then`, "make test", "fi"},
		},
		"step.continue_on_error": {
			build: continueOnError, feature: caps.StepContinueOnError, marker: "# pipemaster:emulated step.continue_on_error " + stepPathOf("build"), level: caps.Emulated,
			want: []string{"make test || true", "make lint || true"},
		},
		"step.env": {
			build: env, feature: caps.StepEnv, marker: "# pipemaster:emulated step.env " + stepPathOf("build"), level: caps.Emulated,
			want: []string{`export A="one"`, `export SHA="$CI_COMMIT_SHA"`},
		},
		"step.workdir": {
			build: workdir, feature: caps.StepWorkdir, marker: "# pipemaster:emulated step.workdir " + stepPathOf("build"), level: caps.Emulated,
			want: []string{`cd "sub/dir" || exit 1`},
		},
		"step.all": {
			build: all, feature: caps.StepIf, marker: "# pipemaster:emulated step.workdir " + stepPathOf("build"), level: caps.Emulated,
			want: []string{"# pipemaster:emulated step.env " + stepPathOf("build"), "# pipemaster:emulated step.continue_on_error " + stepPathOf("build"), `cd "sub" || exit 1`, "make test || true"},
		},
		"job.outputs": {
			build: outputs, feature: caps.JobOutputs, marker: "# pipemaster:emulated job.outputs " + glJobPath, level: caps.Emulated,
			want: []string{`echo "ver=v1-$CI_COMMIT_SHA" >> outputs.env`, "dotenv: outputs.env"},
		},
		"job.matrix": {
			build: matrix, feature: caps.JobMatrix, marker: "# pipemaster:emulated job.matrix " + glJobPath, level: caps.Emulated,
			want: []string{"parallel:", "matrix:"},
		},
		"pipeline.concurrency": {
			build: concurrency, feature: caps.PipelineConcurrency, marker: "# pipemaster:emulated pipeline.concurrency pipeline/gitlab-ci", level: caps.Emulated,
			want: []string{"resource_group: deploy"},
		},
		"pipeline.inputs": {
			build: inputs, feature: caps.PipelineInputs, marker: "# pipemaster:emulated pipeline.inputs pipeline/gitlab-ci", level: caps.Emulated,
			want: []string{"target:", "description: where to deploy"},
		},
	}
}

func TestEmulations(t *testing.T) {
	for name, c := range emulCases() {
		t.Run(name, func(t *testing.T) {
			p := c.build()
			files, rep := emitFiles(t, p)
			doc := string(files[rootFile])
			if !strings.Contains(doc, c.marker) {
				t.Errorf("marker %q missing:\n%s", c.marker, doc)
			}
			for _, w := range c.want {
				if !strings.Contains(doc, w) {
					t.Errorf("want %q in:\n%s", w, doc)
				}
			}
			if err := validate.File("gitlab", rootFile, files[rootFile]); err != nil {
				t.Errorf("schema: %v\n%s", err, doc)
			}
			if !reportHas(rep, c.feature, c.level) {
				t.Errorf("report lacks %s at %s: %+v", c.feature, c.level, rep.Entries)
			}
			back, _, err := gitlabParse(files)
			if err != nil {
				t.Fatalf("reparse: %v\n%s", err, doc)
			}
			if ok, diff := ir.Equal(p, back); !ok {
				t.Errorf("Parse(Emit(ir)) does not restore the IR:\n%s\n--- emitted ---\n%s", diff, doc)
			}
			if strings.Contains(doc, "marker not collapsed") {
				t.Errorf("marker left uncollapsed:\n%s", doc)
			}
		})
	}
}

func reportHas(rep *report.Report, f caps.Feature, lvl caps.Level) bool {
	for _, e := range rep.Entries {
		if e.Feature == f && e.Level == lvl {
			return true
		}
	}
	return false
}

func TestEveryNonNativeCapIsEmulatedOrLost(t *testing.T) {
	tested := map[caps.Feature]bool{}
	for _, c := range emulCases() {
		tested[c.feature] = true
	}
	tested[caps.ProjectMultiPipeline] = true // TestMultiPipelineSharesOneFile
	tested[caps.StepImage] = true            // TestImageSplitChainsJobsAndJoinsThemBack
	tested[caps.StepEnv], tested[caps.StepWorkdir], tested[caps.StepContinueOnError] = true, true, true
	for _, f := range []caps.Feature{caps.TriggerSchedule, caps.TriggerAPI, caps.JobEnvironmentApproval} {
		tested[f] = true
	}
	for f, s := range newProvider().(interface{ Caps() caps.Matrix }).Caps() {
		if s.Level == caps.Native || s.Level == caps.Lost {
			continue
		}
		if !tested[f] {
			t.Errorf("%s is %s but has no emulation or external test", f, s.Level)
		}
	}
}

func TestExternalEntries(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.Environment = &ir.Environment{Name: "prod", Approval: true}
	p := glProject("gitlab", j)
	p.Pipelines[0].Triggers = append(p.Pipelines[0].Triggers, ir.Trigger{Kind: ir.TriggerAPI})
	_, rep := emitFiles(t, p)
	for _, f := range []caps.Feature{caps.JobEnvironmentApproval, caps.TriggerAPI} {
		if !reportHas(rep, f, caps.External) {
			t.Errorf("want an external %s entry: %+v", f, rep.Entries)
		}
	}
}

func TestLostFeaturesAreReported(t *testing.T) {
	st := glStep("make")
	st.Timeout = durOf(60)
	st.Shell = "pwsh"
	j := glJob("build", st)
	p := glProject("gitlab", j)
	p.Pipelines[0].Permissions = &ir.Permissions{Scopes: map[string]string{"contents": "read"}}
	_, rep := emitFiles(t, p)
	for _, f := range []caps.Feature{caps.StepTimeout, caps.StepShell, caps.PipelinePermissions} {
		if !reportHas(rep, f, caps.Lost) {
			t.Errorf("want a lost %s entry: %+v", f, rep.Entries)
		}
	}
}

func durOf(seconds int) *ir.Duration {
	d := ir.Duration(int64(seconds) * 1_000_000_000)
	return &d
}

func TestRetryNative(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.Retry = &ir.Retry{Attempts: 3, On: []string{"runner_system_failure"}}
	p := glProject("gitlab", j)
	back, files := roundTrip(t, p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("%s\n%s", diff, dumpFiles(files))
	}
	if !strings.Contains(string(files[rootFile]), "max: 2") {
		t.Errorf("Attempts 3 is retry max 2:\n%s", files[rootFile])
	}
	j.Retry = &ir.Retry{Attempts: 4, On: []string{"made_up"}}
	_, rep := emitFiles(t, glProject("gitlab", j))
	if !reportHas(rep, caps.JobRetry, caps.Lost) {
		t.Errorf("attempts over 3 and unknown conditions are lost: %+v", rep.Entries)
	}
}

func TestFinallyOnFailure(t *testing.T) {
	fin := glJob("cleanup", glStep("rm -rf tmp"))
	fin.If = expr.Call{Name: "failure"}
	fin.Needs = []ir.Need{{JobID: "build"}}
	fin.Ext = ir.Ext{"gitlab": map[string]any{"stage": ".post", "_derived_needs": true}}
	p := glProject("gitlab", glJob("build", glStep("make")))
	p.Pipelines[0].Finally = []*ir.Job{fin}
	back, files := roundTrip(t, p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Fatalf("%s\n%s", diff, dumpFiles(files))
	}
	if !strings.Contains(string(files[rootFile]), "when: on_failure") {
		t.Errorf("failure() finally job is when: on_failure:\n%s", files[rootFile])
	}
}

func TestFinallyFromGHAUsesPostStage(t *testing.T) {
	fin := glJob("cleanup", glStep("rm -rf tmp"))
	fin.If = expr.Call{Name: "always"}
	fin.Needs = []ir.Need{{JobID: "build"}}
	p := glProject("gha", glJob("build", glStep("make")))
	p.Pipelines[0].Finally = []*ir.Job{fin}
	doc := emitDoc(t, p)
	if !strings.Contains(doc, "stage: .post") || !strings.Contains(doc, "when: always") {
		t.Errorf("finally job needs stage .post and when: always:\n%s", doc)
	}
	assertStagesRespectNeeds(t, doc)
}

func TestFailSafeJobCondition(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.If = expr.Call{Name: "toJSON", Args: []expr.Expr{strLit("x")}}
	files, rep := emitFiles(t, glProject("gha", j))
	doc := string(files[rootFile])
	if !strings.Contains(doc, "when: never") {
		t.Errorf("an inexpressible condition disables the job:\n%s", doc)
	}
	if !strings.Contains(doc, "# pipemaster:lost") {
		t.Errorf("the canonical condition text must stay in a comment:\n%s", doc)
	}
	if !reportHas(rep, caps.JobIf, caps.Lost) {
		t.Errorf("want lost job.if: %+v", rep.Entries)
	}
	found := false
	for _, e := range rep.Entries {
		found = found || strings.Contains(e.Detail, "condition not expressible in GitLab CI; job disabled")
	}
	if !found {
		t.Errorf("detail text missing: %+v", rep.Entries)
	}
	if err := validate.File("gitlab", rootFile, files[rootFile]); err != nil {
		t.Error(err)
	}
}

func TestFailSafeStepCondition(t *testing.T) {
	st := glStep("make")
	st.If = expr.Call{Name: "toJSON", Args: []expr.Expr{strLit("x")}}
	files, rep := emitFiles(t, glProject("gha", glJob("build", st)))
	doc := string(files[rootFile])
	if !strings.Contains(doc, "if false; then") || !strings.Contains(doc, "# pipemaster:lost") {
		t.Errorf("an inexpressible step condition skips the step:\n%s", doc)
	}
	if !reportHas(rep, caps.StepIf, caps.Lost) {
		t.Errorf("want lost step.if: %+v", rep.Entries)
	}
}

func TestGHAConditionIsTranslated(t *testing.T) {
	j := glJob("build", glStep("make"))
	j.If = eq(expr.Ref{Path: []string{expr.CtxEvent}}, strLit("push"))
	doc := emitDoc(t, glProject("gha", j))
	if !strings.Contains(doc, `$CI_PIPELINE_SOURCE =~ /(?i)^push$/`) && !strings.Contains(doc, `$CI_PIPELINE_SOURCE =~ /^push$/i`) {
		t.Errorf("GHA event comparison must become a case-insensitive GitLab regex:\n%s", doc)
	}
}

func TestStatusFunctions(t *testing.T) {
	always := glJob("build", glStep("make"))
	always.If = expr.Call{Name: "always"}
	if doc := emitDoc(t, glProject("gha", always)); !strings.Contains(doc, "when: always") {
		t.Errorf("always() is when: always:\n%s", doc)
	}
	cancelled := glJob("build", glStep("make"))
	cancelled.If = expr.Call{Name: "cancelled"}
	files, rep := emitFiles(t, glProject("gha", cancelled))
	if !strings.Contains(string(files[rootFile]), "when: never") || !reportHas(rep, caps.JobIf, caps.Lost) {
		t.Errorf("cancelled() fails safe:\n%s\n%+v", files[rootFile], rep.Entries)
	}
}

func TestOpaqueRulesRestored(t *testing.T) {
	p := gitlabFixture(t, "expr.opaque")
	doc := emitDoc(t, p)
	if !strings.Contains(doc, "rules:") {
		t.Errorf("opaque gitlab rules restore as rules:\n%s", doc)
	}
	j := glJob("build", glStep("make"))
	j.If = expr.Opaque{Dialect: "gha", Text: "github.event.x"}
	files, rep := emitFiles(t, glProject("gha", j))
	if !strings.Contains(string(files[rootFile]), "when: never") || !reportHas(rep, caps.ExprOpaque, caps.Lost) {
		t.Errorf("a gha opaque expression is lost:\n%s\n%+v", files[rootFile], rep.Entries)
	}
}

func TestInterpolationLostLineIsCommentedOut(t *testing.T) {
	j := glJob("build", glStep("echo ${{ steps.x.outputs.y }} done", "echo ok"))
	files, rep := emitFiles(t, glProject("gha", j))
	doc := string(files[rootFile])
	if strings.Contains(doc, "${{") {
		t.Errorf("no raw expression may survive:\n%s", doc)
	}
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, "echo") && strings.Contains(line, "done") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			t.Errorf("the line with the lost expression must be commented out, got %q", line)
		}
	}
	if !strings.Contains(doc, "# pipemaster:lost") || !strings.Contains(doc, "echo ok") {
		t.Errorf("lost line is a comment and later lines stay:\n%s", doc)
	}
	if !reportHas(rep, caps.ScriptInterpolation, caps.Lost) {
		t.Errorf("want lost script.interpolation: %+v", rep.Entries)
	}
}

func TestActionsMapToJobKeywords(t *testing.T) {
	doc := emitDoc(t, ghaFixture(t, "step.action"))
	for _, want := range []string{`GIT_DEPTH: "0"`, "image: golang:1.24", "# pipemaster:lost step local uses"} {
		if !strings.Contains(doc, want) {
			t.Errorf("want %q in\n%s", want, doc)
		}
	}
	_, rep := emitFiles(t, ghaFixture(t, "step.action"))
	if !reportHas(rep, caps.StepAction, caps.Lost) {
		t.Errorf("unmapped inputs and unknown actions are lost: %+v", rep.Entries)
	}
	if !reportHas(rep, caps.StepAction, caps.Emulated) {
		t.Errorf("image selection is emulated: %+v", rep.Entries)
	}
}

func ghaUses(id, repoName string, with map[string]string) *ir.Step {
	w := map[string]ir.Value{}
	for k, v := range with {
		w[k] = ir.Value{Lit: v}
	}
	return &ir.Step{ID: id, Kind: ir.StepAction, With: w, Action: &ir.ActionRef{Kind: ir.ActionRepo, Owner: "actions", Repo: repoName, Ref: "v4"}}
}

func TestCacheAndArtifactActions(t *testing.T) {
	build := glJob("build",
		ghaUses("cache", "cache", map[string]string{"path": "~/.cache/go\nvendor", "key": "go-${{ hashFiles('**/go.sum') }}"}),
		glStep("make"),
		ghaUses("up", "upload-artifact", map[string]string{"name": "bin", "path": "out/\n!out/tmp", "retention-days": "3"}),
	)
	test := glJob("test", ghaUses("down", "download-artifact", map[string]string{"name": "bin"}), glStep("make test"))
	test.Needs = []ir.Need{{JobID: "build"}}
	doc := emitDoc(t, glProject("gha", build, test))
	for _, want := range []string{"cache:", "files:", "**/go.sum", "prefix: go", "expire_in: 3 days", "exclude:", "artifacts: true", "name: bin"} {
		if !strings.Contains(doc, want) {
			t.Errorf("want %q in\n%s", want, doc)
		}
	}
	if err := validate.File("gitlab", rootFile, []byte(doc)); err != nil {
		t.Errorf("%v\n%s", err, doc)
	}
	assertStagesRespectNeeds(t, doc)
}

func TestCacheKeyFilesLimit(t *testing.T) {
	build := glJob("build", ghaUses("cache", "cache", map[string]string{"path": "x", "key": "${{ hashFiles('a', 'b', 'c') }}"}))
	_, rep := emitFiles(t, glProject("gha", build))
	if !reportHas(rep, caps.StepAction, caps.Lost) {
		t.Errorf("more than two key files is lost: %+v", rep.Entries)
	}
}

func TestMarkerHelperRoundTrip(t *testing.T) {
	m := emitkit.Marker(caps.StepIf, "pipeline/gitlab-ci/job/build/step/script")
	if got := emitkit.ParseMarkers([]byte(m)); len(got) != 1 {
		t.Errorf("marker %q not recognised", m)
	}
}

func TestSecondUploadWithOtherExpiryIsReported(t *testing.T) {
	p := glProject("gha", glJob("build", glStep("make"),
		ghaUses("up1", "upload-artifact", map[string]string{"name": "bin", "path": "out", "retention-days": "3"}),
		ghaUses("up2", "upload-artifact", map[string]string{"name": "bin", "path": "docs", "retention-days": "9"})))
	_, rep := emitFiles(t, p)
	if !hasEntry(rep, "expire_in") {
		t.Errorf("the dropped second expiry is not reported: %+v", rep.Entries)
	}
}

func TestMovedAlwaysStepWithEnvKeepsItsID(t *testing.T) {
	cleanup := &ir.Step{
		ID: "cleanup", Kind: ir.StepScript, Script: []string{"rm -rf tmp"}, If: expr.Call{Name: "always"},
		Env: []ir.Var{{Name: "DIR", Value: ir.Value{Lit: "tmp"}}},
	}
	back, files := roundTrip(t, glProject("gitlab", glJob("build", glStep("make"), cleanup)))
	var got *ir.Step
	for _, st := range back.Pipelines[0].Jobs[0].Steps {
		if st.ID == "cleanup" {
			got = st
		}
	}
	if got == nil || got.If == nil || expr.String(got.If) != "always()" {
		t.Fatalf("the moved step is not rebuilt from its marker: %+v\n%s", back.Pipelines[0].Jobs[0].Steps, dumpFiles(files))
	}
}
