package woodpecker_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const editedSource = `on: push
jobs:
  b:
    runs-on: ubuntu-latest
    continue-on-error: true
    env:
      KEEP: one
    strategy:
      matrix:
        go: ['1.21', '1.22']
        include:
          - go: '1.22'
            experimental: 'true'
    steps:
      - run: echo a
      - run: echo b
`

// editedJob converts the source to Woodpecker, rewrites the file text with edit and parses it back.
func editedJob(t *testing.T, edit func(string) string) (*ir.Job, *report.Report) {
	t.Helper()
	files, _ := emitFiles(t, parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": editedSource}))
	raw := map[string]string{}
	for n, b := range files {
		out := edit(b)
		if out == b {
			t.Fatalf("edit changed nothing in %s:\n%s", n, b)
		}
		raw[n] = out
	}
	src, err := provider.Get("woodpecker")
	if err != nil {
		t.Fatal(err)
	}
	p, rep, err := src.Parse(repo(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0].Jobs[0], rep
}

func ignoredMarker(rep *report.Report, f caps.Feature) bool {
	for _, e := range rep.Entries {
		if e.Feature == f && e.Level == caps.Lost && strings.Contains(e.Detail, "marker ignored") && strings.Contains(e.Detail, "edited after conversion") {
			return true
		}
	}
	return false
}

func TestEditedMatrixKeepsTheParsedValue(t *testing.T) {
	j, rep := editedJob(t, func(s string) string { return strings.Replace(s, `go: "1.21"`, `go: "1.20"`, 1) })
	if j.Matrix == nil || len(j.Matrix.Axes) != 0 {
		t.Fatalf("the marker restored the matrix over the edit: %+v", j.Matrix)
	}
	if got := j.Matrix.Include[0][0].Value; got != "1.20" {
		t.Errorf("first entry = %q, want the edited 1.20", got)
	}
	if !ignoredMarker(rep, caps.JobMatrix) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

// bigMatrixRoundTrip converts a one-axis matrix of 70 values, which the emitter writes as include entries cut at 64.
func bigMatrixRoundTrip(t *testing.T, edit func(string) string) (*ir.Job, *report.Report) {
	t.Helper()
	vals := make([]string, 70)
	for i := range vals {
		vals[i] = fmt.Sprintf("'v%02d'", i+1)
	}
	src := "on: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    strategy:\n      matrix:\n        n: [" +
		strings.Join(vals, ", ") + "]\n    steps:\n      - run: echo a\n"
	files, _ := emitFiles(t, parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": src}))
	raw := map[string]string{}
	for n, body := range files {
		raw[n] = edit(body)
	}
	wp, err := provider.Get("woodpecker")
	if err != nil {
		t.Fatal(err)
	}
	p, rep, err := wp.Parse(repo(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0].Jobs[0], rep
}

func TestUneditedTruncatedMatrixRestoresInFull(t *testing.T) {
	j, rep := bigMatrixRoundTrip(t, func(s string) string { return s })
	if j.Matrix == nil || len(j.Matrix.Axes) != 1 || len(ir.Expand(j.Matrix)) != 70 {
		t.Errorf("the full matrix was not restored: %+v", j.Matrix)
	}
	if ignoredMarker(rep, caps.JobMatrix) {
		t.Errorf("a false marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedTruncatedMatrixStillReportsTheEdit(t *testing.T) {
	j, rep := bigMatrixRoundTrip(t, func(s string) string {
		return s[:strings.LastIndex(s, "v05")] + "v99" + s[strings.LastIndex(s, "v05")+3:]
	})
	if j.Matrix == nil || len(j.Matrix.Axes) != 0 {
		t.Errorf("the marker restored the matrix over the edit: %+v", j.Matrix)
	}
	if !ignoredMarker(rep, caps.JobMatrix) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedAllowFailureKeepsTheParsedValue(t *testing.T) {
	j, rep := editedJob(t, func(s string) string { return strings.Replace(s, "failure: ignore", "", 1) })
	if j.AllowFailure {
		t.Error("the marker restored allow_failure although a step lost failure: ignore")
	}
	if !ignoredMarker(rep, caps.JobAllowFailure) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func TestEditedEnvKeepsTheParsedValue(t *testing.T) {
	j, rep := editedJob(t, func(s string) string { return strings.ReplaceAll(s, "    environment:\n      KEEP: one\n", "") })
	if len(j.Env) != 0 {
		t.Errorf("the marker restored the job env over the edit: %+v", j.Env)
	}
	if !ignoredMarker(rep, caps.JobEnv) {
		t.Errorf("no marker-ignored entry: %+v", rep.Entries)
	}
}

func parseWoodpecker(t *testing.T, body string) (*ir.Job, *report.Report) {
	t.Helper()
	src, err := provider.Get("woodpecker")
	if err != nil {
		t.Fatal(err)
	}
	p, rep, err := src.Parse(repo(map[string]string{".woodpecker/ci.yaml": body}))
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0].Jobs[0], rep
}

func TestRuntimeVariablesInScriptsBecomeReferencesOrAreReported(t *testing.T) {
	j, rep := parseWoodpecker(t, `steps:
  - name: s
    image: alpine
    commands:
      - gh pr merge "$CI_COMMIT_PULL_REQUEST" --repo "$CI_REPO"
      - echo $$CI_COMMIT_SHA $${CI_COMMIT_BRANCH} $HOME
      - echo $CI_PIPELINE_NUMBER $$CI_STEP_NAME $${CI_BUILD_STATUS}
`)
	got := j.Steps[0].Script
	want := []string{
		`gh pr merge "${{ pr.number }}" --repo "${{ repo }}"`,
		`echo ${{ sha }} ${CI_COMMIT_BRANCH} $HOME`,
		`echo $CI_PIPELINE_NUMBER $CI_STEP_NAME ${CI_BUILD_STATUS}`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, name := range []string{"CI_PIPELINE_NUMBER", "CI_STEP_NAME", "CI_BUILD_STATUS"} {
		found := false
		for _, e := range rep.Entries {
			found = found || e.Feature == caps.ScriptInterpolation && e.Level == caps.Lost && strings.Contains(e.Detail, name)
		}
		if !found {
			t.Errorf("no script.interpolation entry for %s: %+v", name, rep.Entries)
		}
	}
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "HOME") || strings.Contains(e.Detail, "CI_REPO") {
			t.Errorf("unexpected entry %+v", e)
		}
	}
}

func TestVariableReferenceIsBoundAndReported(t *testing.T) {
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${{ vars.HOST }} ${{ inputs.mode }}\n"})
	files, rep := emitFiles(t, p)
	var body string
	for _, b := range files {
		body = b
	}
	if !strings.Contains(body, "HOST:\n        from_secret: HOST") {
		t.Errorf("vars.HOST is not bound to a secret:\n%s", body)
	}
	var host, mode bool
	for _, e := range rep.Entries {
		if e.Feature != caps.ScriptInterpolation || e.Level != caps.External {
			continue
		}
		host = host || strings.Contains(e.Detail, "create the secret HOST")
		mode = mode || strings.Contains(e.Detail, "input mode")
	}
	if !host || !mode {
		t.Errorf("host note %v, mode note %v: %+v", host, mode, rep.Entries)
	}
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = b
	}
	back := parseProject(t, "woodpecker", raw).Pipelines[0].Jobs[0].Steps[0]
	if len(back.Env) != 0 || back.Script[0] != "echo ${{ vars.HOST }} ${{ inputs.mode }}" {
		t.Errorf("round trip lost the references: env %+v script %q", back.Env, back.Script)
	}
}

const pluginSource = `steps:
  - name: upload
    image: woodpeckerci/plugin-s3
    settings:
      bucket: my-bucket
      access_key:
        from_secret: s3_key
`

func TestPluginSettingsReachGHAAsPluginVariablesAndRoundTrip(t *testing.T) {
	p := parseProject(t, "woodpecker", map[string]string{".woodpecker/rel.yaml": pluginSource})
	gha, err := provider.Get("gha")
	if err != nil {
		t.Fatal(err)
	}
	files, rep, err := gha.Emit(p)
	if err != nil {
		t.Fatal(err)
	}
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = string(b)
		for _, want := range []string{"PLUGIN_BUCKET: my-bucket", "PLUGIN_ACCESS_KEY: ${{ secrets.s3_key }}"} {
			if !strings.Contains(string(b), want) {
				t.Errorf("GHA file lacks %q:\n%s", want, b)
			}
		}
		if strings.Contains(string(b), "with:\n        bucket") || strings.Contains(string(b), "access_key:") && !strings.Contains(string(b), "PLUGIN_ACCESS_KEY") {
			t.Errorf("settings are still written as inputs:\n%s", b)
		}
	}
	if !reportHas(rep, caps.StepImage, caps.Emulated) || !reportDetailHas(rep, "INPUT_") {
		t.Errorf("the emulation caveat is missing: %+v", rep.Entries)
	}
	src, err := provider.Get("gha")
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := src.Parse(repo(raw))
	if err != nil {
		t.Fatal(err)
	}
	wpFiles, _ := emitFiles(t, back)
	for _, b := range wpFiles {
		for _, want := range []string{"bucket: my-bucket", "access_key:\n        from_secret: s3_key"} {
			if !strings.Contains(strings.ReplaceAll(b, `\"`, `"`), want) {
				t.Errorf("round trip lost %q:\n%s", want, b)
			}
		}
	}
	st := back.Pipelines[0].Jobs[0].Steps[0]
	if st.Kind != ir.StepPlugin || len(st.Env) != 0 || len(st.With) != 2 {
		t.Errorf("step = kind %v env %+v with %+v", st.Kind, st.Env, st.With)
	}
}

func TestGitLabBranchAndTagRulesTranslateToWoodpecker(t *testing.T) {
	for name, c := range map[string][2]string{
		"branch equality": {`$CI_COMMIT_BRANCH == "main"`, `CI_COMMIT_BRANCH == "main"`},
		"tag truthiness":  {`$CI_COMMIT_TAG`, `CI_COMMIT_TAG != ""`},
	} {
		cond, want := c[0], c[1]
		t.Run(name, func(t *testing.T) {
			p := parseProject(t, "gitlab", map[string]string{".gitlab-ci.yml": "build:\n  script: [make]\n  rules:\n    - if: '" + cond + "'\n"})
			files, rep := emitFiles(t, p)
			for n, b := range files {
				if strings.Contains(b, `evaluate: "false"`) || !strings.Contains(strings.ReplaceAll(b, `\"`, `"`), want) {
					t.Errorf("%s: want evaluate %s:\n%s", n, want, b)
				}
			}
			for _, e := range rep.Entries {
				if e.Level == caps.Lost && strings.Contains(e.Detail, "condition not expressible") {
					t.Errorf("condition reported as lost: %+v", e)
				}
			}
		})
	}
}

func TestLiteralTemplateOpenerDoesNotFailTheGitLabConversion(t *testing.T) {
	p := parseProject(t, "woodpecker", map[string]string{".woodpecker/ci.yaml": "steps:\n  - name: a\n    image: alpine\n    commands:\n      - echo \"$${{ x }}\"\n      - echo ok\n"})
	gl, err := provider.Get("gitlab")
	if err != nil {
		t.Fatal(err)
	}
	files, rep, err := gl.Emit(p)
	if err != nil {
		t.Fatalf("one literal line fails the whole conversion: %v", err)
	}
	for _, b := range files {
		if strings.Contains(string(b), "echo ok") && !strings.Contains(string(b), "# ") && strings.Contains(string(b), "${{") {
			t.Errorf("a raw expression opener is left in the file:\n%s", b)
		}
		if !strings.Contains(string(b), "echo ok") {
			t.Errorf("the other line was dropped:\n%s", b)
		}
	}
	if !reportHas(rep, caps.ScriptInterpolation, caps.Lost) {
		t.Errorf("no Lost entry: %+v", rep.Entries)
	}
}

func TestStepDependsOnReportSaysTheDAGIsNotApplied(t *testing.T) {
	_, rep := parseWoodpecker(t, "steps:\n  - name: b\n    image: alpine\n    depends_on: [a]\n    commands: [echo b]\n  - name: a\n    image: alpine\n    commands: [echo a]\n")
	if !reportDetailHas(rep, "the step DAG is not applied") {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestKeptSubstitutionReportNamesTheWholeForm(t *testing.T) {
	_, rep := parseWoodpecker(t, "steps:\n  - name: s\n    image: alpine\n    commands:\n      - echo ${{ x }}\n")
	if !reportDetailHas(rep, "${{ x }} is a Woodpecker substitution") {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestIgnoredFileReportNamesTheDirectory(t *testing.T) {
	src, err := provider.Get("woodpecker")
	if err != nil {
		t.Fatal(err)
	}
	step := "steps:\n  - name: s\n    image: alpine\n    commands: [echo]\n"
	_, rep, err := src.Parse(repo(map[string]string{".woodpecker/a.yaml": step, ".woodpecker/b.yaml": step, ".woodpecker.yaml": step}))
	if err != nil {
		t.Fatal(err)
	}
	if !reportDetailHas(rep, "Woodpecker reads only the .woodpecker directory") {
		t.Errorf("entries: %+v", rep.Entries)
	}
}

func TestMatrixValueWithYAMLCharactersIsReported(t *testing.T) {
	for name, vals := range map[string]string{"axis": "[ 'a: b', 'c' ]", "plain": "[ '1.21', '1.22' ]"} {
		t.Run(name, func(t *testing.T) {
			p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    strategy:\n      matrix:\n        v: " + vals + "\n    steps:\n      - run: echo ${{ matrix.v }}\n"})
			_, rep := emitFiles(t, p)
			if got := reportDetailHas(rep, "YAML-significant"); got != (name == "axis") {
				t.Errorf("reported = %v: %+v", got, rep.Entries)
			}
		})
	}
}
