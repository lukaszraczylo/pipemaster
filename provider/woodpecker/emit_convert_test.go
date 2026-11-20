package woodpecker_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	wpexpr "github.com/lukaszraczylo/pipemaster/expr/woodpecker"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/report"
)

const gitlabEntrypoint = `build:
  image:
    name: alpine:3
    entrypoint: ["/bin/sh", "-c"]
  script:
    - make
`

const reasonScriptRunner = "a step entrypoint replaces Woodpecker's script runner"

func lostWithReason(rep *report.Report, reason string) bool {
	for _, e := range rep.Entries {
		if e.Level == caps.Lost && strings.Contains(e.Detail, reason) {
			return true
		}
	}
	return false
}

func TestGitLabImageEntrypointIsLostWithReason(t *testing.T) {
	p := parseProject(t, "gitlab", map[string]string{".gitlab-ci.yml": gitlabEntrypoint})
	if len(p.Pipelines[0].Jobs[0].Container.Entrypoint) == 0 {
		t.Fatal("precondition: the GitLab parser must keep the entrypoint")
	}
	files, rep := emitFiles(t, p)
	if !lostWithReason(rep, reasonScriptRunner) {
		t.Errorf("a non-empty job entrypoint must be reported lost: %+v", rep.Entries)
	}
	for n, body := range files {
		if strings.Contains(body, "entrypoint") {
			t.Errorf("%s: the entrypoint must not reach a command step:\n%s", n, body)
		}
	}
}

func TestGitLabEmptyEntrypointIsNotLost(t *testing.T) {
	src := "build:\n  image:\n    name: x\n    entrypoint: [\"\"]\n  script:\n    - make\n"
	p := parseProject(t, "gitlab", map[string]string{".gitlab-ci.yml": src})
	if c := p.Pipelines[0].Jobs[0].Container; c == nil || len(c.Entrypoint) == 0 {
		t.Fatalf("precondition: the entrypoint [\"\"] must reach the IR: %+v", c)
	}
	files, rep := emitFiles(t, p)
	if lostWithReason(rep, reasonScriptRunner) || reportDetailHas(rep, "entrypoint") {
		t.Errorf("an empty entrypoint is already ignored by Woodpecker: %+v", rep.Entries)
	}
	for n, body := range files {
		if strings.Contains(body, "entrypoint") {
			t.Errorf("%s: no entrypoint expected:\n%s", n, body)
		}
	}
}

func reportDetailHas(rep *report.Report, s string) bool {
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, s) {
			return true
		}
	}
	return false
}

// parseProject reads files with the source provider.
func parseProject(t *testing.T, from string, files map[string]string) *ir.Project {
	t.Helper()
	src, err := provider.Get(from)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := src.Parse(repo(files))
	if err != nil {
		t.Fatalf("Parse %s: %v", from, err)
	}
	return p
}

func roundTripTriggers(t *testing.T, on string) (want, got []ir.Trigger, back *ir.Project) {
	t.Helper()
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on:\n" + on + "jobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n"})
	files, _ := emitFiles(t, p)
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = b
	}
	back = parseProject(t, "woodpecker", raw)
	return p.Pipelines[0].Triggers, back.Pipelines[0].Triggers, back
}

func TestGHATagFilterSurvivesWoodpecker(t *testing.T) {
	for name, on := range map[string]string{
		"tags include": "  push:\n    tags: [\"v*\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			want, got, _ := roundTripTriggers(t, on)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("triggers changed:\nwant %+v\ngot  %+v", want, got)
			}
		})
	}
}

func TestBarePushSurvivesWoodpecker(t *testing.T) {
	want, got, _ := roundTripTriggers(t, "  push:\n")
	if len(want) != 2 || !reflect.DeepEqual(want, got) {
		t.Errorf("a bare push is push plus tag:\nwant %+v\ngot  %+v", want, got)
	}
}

func TestTagIncludeAndExcludeIdentity(t *testing.T) {
	p := wpProject("woodpecker", wpJob("ci", wpStep("s", "x")))
	p.Pipelines[0].Triggers = []ir.Trigger{{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}, Exclude: []string{"v0*"}}}}
	files, _ := emitFiles(t, p)
	back := reparse(t, files)
	if !reflect.DeepEqual(p.Pipelines[0].Triggers, back.Pipelines[0].Triggers) {
		t.Errorf("want %+v, got %+v", p.Pipelines[0].Triggers, back.Pipelines[0].Triggers)
	}
}

func TestMultiLineCommandKeepsItsShape(t *testing.T) {
	for name, cmd := range map[string]string{"no newline": "a \\\n b", "one newline": "a \\\n b\n", "two newlines": "a\n\n"} {
		t.Run(name, func(t *testing.T) {
			p := parseProject(t, "woodpecker", map[string]string{".woodpecker.yaml": "steps:\n  - name: s\n    image: x\n    commands:\n      - " + strconv.Quote(cmd) + "\n      - tail\n"})
			files, _ := emitFiles(t, p)
			raw := map[string]string{}
			for n, b := range files {
				raw[n] = b
			}
			back := parseProject(t, "woodpecker", raw).Pipelines[0].Jobs[0].Steps[0]
			if !reflect.DeepEqual(back.Script, p.Pipelines[0].Jobs[0].Steps[0].Script) || !reflect.DeepEqual(back.Ext, p.Pipelines[0].Jobs[0].Steps[0].Ext) {
				t.Errorf("script %q ext %v, want %q ext %v", back.Script, back.Ext, p.Pipelines[0].Jobs[0].Steps[0].Script, p.Pipelines[0].Jobs[0].Steps[0].Ext)
			}
		})
	}
}

func TestDerivedStepNameWithUnspellableExpressionIsReported(t *testing.T) {
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - name: Install ${{ inputs.name }}\n        run: make\n"})
	files, rep := emitFiles(t, p)
	for n, body := range files {
		if strings.Contains(body, "${{") {
			t.Errorf("%s holds a GHA expression:\n%s", n, body)
		}
	}
	if !reportHas(rep, caps.DisplayName, caps.Lost) {
		t.Errorf("the dropped expression must be reported: %+v", rep.Entries)
	}
}

func TestStepEnvWithUnspellableExpressionSurvives(t *testing.T) {
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n        env:\n          A: one\n          TOX: ${{ matrix.tox || format('py{0}', matrix.python) }}\n          C: three\n"})
	want := p.Pipelines[0].Jobs[0].Steps[0].Env
	files, _ := emitFiles(t, p)
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = b
	}
	got := parseProject(t, "woodpecker", raw).Pipelines[0].Jobs[0].Steps[0].Env
	if !reflect.DeepEqual(want, got) {
		t.Errorf("env = %+v, want %+v", got, want)
	}
}

func TestMatrixReferenceWithoutDeclaredAxisStaysMatrix(t *testing.T) {
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    strategy:\n      matrix:\n        lang: ${{ fromJson(needs.x.outputs.langs) }}\n    steps:\n      - run: make ${{ matrix.lang }}\n"})
	files, _ := emitFiles(t, p)
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = b
	}
	got := parseProject(t, "woodpecker", raw).Pipelines[0].Jobs[0].Steps[0].Script
	if !reflect.DeepEqual(got, []string{"make ${{ matrix.lang }}"}) {
		t.Errorf("script = %q", got)
	}
}

func TestStepVariableEqualToTheJobOneSurvives(t *testing.T) {
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on: push\nenv:\n  FLAGS: -w\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n        env:\n          FLAGS: -w\n"})
	files, _ := emitFiles(t, p)
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = b
	}
	back := parseProject(t, "woodpecker", raw)
	if got := back.Pipelines[0].Jobs[0].Steps[0].Env; !reflect.DeepEqual(got, p.Pipelines[0].Jobs[0].Steps[0].Env) {
		t.Errorf("step env = %+v, want %+v", got, p.Pipelines[0].Jobs[0].Steps[0].Env)
	}
}

func ghaTriggers(t *testing.T, on string) (map[string]string, *report.Report) {
	t.Helper()
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on:\n" + on + "jobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n"})
	return emitFiles(t, p)
}

func TestGHAQuantifierFilterBecomesEvaluate(t *testing.T) {
	for name, c := range map[string]struct{ on, key, want string }{
		"tag plus":        {"  push:\n    tags: ['v[0-9]+.[0-9]+']\n", "CI_COMMIT_TAG", `CI_COMMIT_TAG matches "^v[0-9]+\\.[0-9]+$$"`},
		"branch plus":     {"  push:\n    branches: ['release-[0-9]+']\n", "CI_COMMIT_BRANCH", `CI_COMMIT_BRANCH matches "^release-[0-9]+$$"`},
		"branch question": {"  push:\n    branches: ['colou?r']\n", "CI_COMMIT_BRANCH", `CI_COMMIT_BRANCH matches "^colou?r$$"`},
	} {
		t.Run(name, func(t *testing.T) {
			want, got, _ := roundTripTriggers(t, c.on)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("triggers changed:\nwant %+v\ngot  %+v", want, got)
			}
			files, rep := ghaTriggers(t, c.on)
			body := files[".woodpecker/b.yaml"]
			if !strings.Contains(body, c.want) {
				t.Errorf("missing %s in\n%s", c.want, body)
			}
			if strings.Contains(body, "refs/tags/v[0-9]+") || strings.Contains(body, "branch:\n") && strings.Contains(body, "[0-9]+") && !strings.Contains(body, "evaluate") {
				t.Errorf("a pattern doublestar misreads was written as a glob:\n%s", body)
			}
			if !reportDetailHas(rep, "regular expression test on") {
				t.Errorf("the report must say the filter became a regular expression: %+v", rep.Entries)
			}
		})
	}
}

func TestGHAQuantifierFilterKeepsTheJobCondition(t *testing.T) {
	p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on:\n  push:\n    branches: ['r-[0-9]+']\n  pull_request:\n    branches: ['r-[0-9]+']\njobs:\n  b:\n    runs-on: ubuntu-latest\n    if: github.sha == 'abc'\n    steps:\n      - run: make\n"})
	files, _ := emitFiles(t, p)
	raw := map[string]string{}
	for n, b := range files {
		raw[n] = b
	}
	back := parseProject(t, "woodpecker", raw)
	if !reflect.DeepEqual(back.Pipelines[0].Triggers, p.Pipelines[0].Triggers) {
		t.Errorf("triggers changed: %+v", back.Pipelines[0].Triggers)
	}
	got := expr.String(back.Pipelines[0].Jobs[0].If)
	if !strings.Contains(got, "sha") || strings.Contains(got, "branch") {
		t.Errorf("job condition = %s, want only the sha test: the filter text must not leak into it", got)
	}
}

func TestGHAQuantifierPathFilterDisablesTheWorkflow(t *testing.T) {
	files, rep := ghaTriggers(t, "  push:\n    paths: ['docs/a+.md']\n")
	if !reportHas(rep, caps.TriggerFilterPaths, caps.Lost) {
		t.Errorf("a path filter that cannot be written must be lost: %+v", rep.Entries)
	}
	for n, b := range files {
		if !strings.Contains(b, `evaluate: "false"`) {
			t.Errorf("%s must never run, not silently never match the path:\n%s", n, b)
		}
	}
}

func TestInexactTagFilterReportKeepsEveryCaveat(t *testing.T) {
	_, rep := ghaTriggers(t, "  push:\n    tags: ['v**']\n")
	if !reportDetail(rep, caps.TriggerFilterTags, "different meaning in doublestar") || !reportDetail(rep, caps.TriggerFilterTags, "tag name filter") {
		t.Errorf("both caveats must survive in the report: %+v", rep.Entries)
	}
}

// mapEnv is a run as the evaluator sees it: canonical names to values.
type mapEnv map[string]expr.Value

func (e mapEnv) Lookup(p []string) (expr.Value, bool) { v, ok := e[strings.Join(p, ".")]; return v, ok }
func (mapEnv) Status() expr.Status                    { return expr.Success }

func TestRegexFilterGuardsEveryPatternWithTheJobCondition(t *testing.T) {
	for name, c := range map[string]struct {
		env          mapEnv
		on, wantEval string
	}{
		"two branch patterns on pull_request": {
			mapEnv{"branch": "r-1", "sha": "zzz"},
			"  pull_request:\n    branches: ['r-[0-9]+', 'q-[0-9]+']\n",
			`(CI_COMMIT_BRANCH matches "^r-[0-9]+$$" || CI_COMMIT_BRANCH matches "^q-[0-9]+$$") && (CI_COMMIT_SHA matches "(?i)^abc$$")`,
		},
		"two tag patterns": {
			mapEnv{"tag": "v1", "sha": "zzz"},
			"  push:\n    tags: ['v[0-9]+', 'w[0-9]+']\n",
			`(CI_COMMIT_TAG matches "^v[0-9]+$$" || CI_COMMIT_TAG matches "^w[0-9]+$$") && (CI_COMMIT_SHA matches "(?i)^abc$$")`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := parseProject(t, "gha", map[string]string{".github/workflows/ci.yml": "on:\n" + c.on + "jobs:\n  b:\n    runs-on: ubuntu-latest\n    if: github.sha == 'abc'\n    steps:\n      - run: make\n"})
			files, _ := emitFiles(t, p)
			body := files[".woodpecker/b.yaml"]
			if !strings.Contains(body, "evaluate: "+c.wantEval) {
				t.Fatalf("evaluate text differs, want %s in\n%s", c.wantEval, body)
			}
			x, err := wpexpr.Parse(strings.ReplaceAll(c.wantEval, "$$", "$"))
			if err != nil {
				t.Fatal(err)
			}
			v, err := expr.Eval(x, wpexpr.UnsetEnv{Env: c.env}, wpexpr.Semantics)
			if err != nil || v != false {
				t.Errorf("a first-pattern match with a false job condition must be false, got %v (%v)", v, err)
			}
			raw := map[string]string{}
			for n, b := range files {
				raw[n] = b
			}
			back := parseProject(t, "woodpecker", raw)
			if !reflect.DeepEqual(back.Pipelines[0].Triggers, p.Pipelines[0].Triggers) {
				t.Errorf("triggers changed:\nwant %+v\ngot  %+v", p.Pipelines[0].Triggers, back.Pipelines[0].Triggers)
			}
			got := expr.String(back.Pipelines[0].Jobs[0].If)
			if !strings.Contains(got, "sha") || strings.Contains(got, "branch") || strings.Contains(got, "tag") {
				t.Errorf("job condition = %s, want only the sha test", got)
			}
		})
	}
}
