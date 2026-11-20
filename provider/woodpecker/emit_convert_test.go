package woodpecker_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
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
