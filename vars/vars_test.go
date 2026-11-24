package vars_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/vars"
)

func TestRefs(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
		shell      bool
	}{
		{"bare", `echo $CI_COMMIT_SHA`, []string{"CI_COMMIT_SHA"}, true},
		{"braced", `echo ${CI_COMMIT_SHA}`, []string{"CI_COMMIT_SHA"}, true},
		{"default", `echo ${CI_COMMIT_TAG:-none}`, []string{"CI_COMMIT_TAG"}, true},
		{"double quoted", `echo "dir=$CI_PROJECT_DIR/x"`, []string{"CI_PROJECT_DIR"}, true},
		{"single quoted", `echo '$CI_COMMIT_SHA'`, nil, true},
		{"single inside double", `echo "it's $CI_JOB_ID"`, []string{"CI_JOB_ID"}, true},
		{"escaped", `echo \$CI_COMMIT_SHA`, nil, true},
		{"doubled dollar", `echo $$CI_COMMIT_SHA`, nil, true},
		{"comment", "echo hi # $CI_COMMIT_SHA", nil, true},
		{"longer name", `echo $CI_COMMIT_SHA_X`, []string{"CI_COMMIT_SHA_X"}, true},
		{"command substitution", `echo $(date)`, nil, true},
		{"value text ignores quotes", `'$CI_PROJECT_DIR'/x`, []string{"CI_PROJECT_DIR"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vars.Refs(c.text, c.shell); !slices.Equal(got, c.want) {
				t.Errorf("Refs(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

func TestTableLoadsAndCitesEveryProvider(t *testing.T) {
	tab, err := vars.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{provider.GitLab, provider.GHA, provider.Drone, provider.Woodpecker, provider.Tekton, provider.Argo, provider.Concourse} {
		if !strings.HasPrefix(tab.Docs()[id], "https://") {
			t.Errorf("no documentation URL for %s", id)
		}
	}
	if len(tab.IDs()) < 20 {
		t.Errorf("table has %d entries", len(tab.IDs()))
	}
}

func shellStep(line string) *ir.Project {
	pl := &ir.Pipeline{ID: "p", Jobs: []*ir.Job{{ID: "j", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{line}}}}}}
	return &ir.Project{Origin: ir.Origin{Provider: provider.GitLab}, Pipelines: []*ir.Pipeline{pl}}
}

func prepared(t *testing.T, line, target string) string {
	t.Helper()
	p, _ := vars.Prepare(shellStep(line), target)
	return p.Pipelines[0].Jobs[0].Steps[0].Script[0]
}

func TestPrepareHonoursShellQuoting(t *testing.T) {
	for _, c := range []struct {
		name, line string
		want       bool
	}{
		{"bare", `echo $CI_PROJECT_DIR`, true},
		{"braced", `echo ${CI_PROJECT_DIR}`, true},
		{"default", `echo ${CI_PROJECT_DIR:-d}`, true},
		{"double quoted", `echo "$CI_PROJECT_DIR/x"`, true},
		{"single quoted", `echo '$CI_PROJECT_DIR'`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Contains(prepared(t, c.line, provider.Drone), "export CI_PROJECT_DIR=")
			if got != c.want {
				t.Errorf("prelude = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPrepareLeavesNativeNamesAlone(t *testing.T) {
	if got := prepared(t, `echo $CI_COMMIT_SHA`, provider.GitLab); got != `echo $CI_COMMIT_SHA` {
		t.Errorf("native name changed: %q", got)
	}
}

func TestPrepareGuardsLostNames(t *testing.T) {
	got := prepared(t, `echo $CI_JOB_ID`, provider.Drone)
	want := `: "${CI_JOB_ID:?pipemaster: lost: variable CI_JOB_ID has no drone source}"`
	if !strings.Contains(got, want) {
		t.Errorf("no guard in %q", got)
	}
}

func TestStripUndoesPrepare(t *testing.T) {
	line := `echo "$CI_PROJECT_DIR $CI_JOB_ID"`
	p, _ := vars.Prepare(shellStep(line), provider.Drone)
	vars.Strip(p)
	if got := p.Pipelines[0].Jobs[0].Steps[0].Script[0]; got != line {
		t.Errorf("Strip left %q", got)
	}
}

func TestPrepareDoesNotChangeItsInput(t *testing.T) {
	in := shellStep(`echo $CI_PROJECT_DIR`)
	vars.Prepare(in, provider.Drone)
	if got := in.Pipelines[0].Jobs[0].Steps[0].Script[0]; got != `echo $CI_PROJECT_DIR` {
		t.Errorf("input changed: %q", got)
	}
}

func TestPrepareReExportsEnvValues(t *testing.T) {
	in := shellStep(`echo $GOPATH`)
	in.Pipelines[0].Env = []ir.Var{{Name: "GOPATH", Value: ir.Value{Lit: "$CI_PROJECT_DIR/.GOPATH"}}}
	p, _ := vars.Prepare(in, provider.Woodpecker)
	got := p.Pipelines[0].Jobs[0].Steps[0].Script[0]
	for _, want := range []string{`export CI_PROJECT_DIR="${CI_WORKSPACE}"`, `export GOPATH="$CI_PROJECT_DIR/.GOPATH"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if got := prepared(t, `echo $x`, provider.Woodpecker); got != `echo $x` {
		t.Errorf("plain script changed: %q", got)
	}
}

func TestRefsHeredoc(t *testing.T) {
	for _, c := range []struct {
		name, text string
		want       []string
	}{
		{"quoted body", "cat <<'EOT'\n$CI_COMMIT_SHA\nEOT", nil},
		{"unquoted body", "cat <<EOT\n$CI_COMMIT_SHA\nEOT", []string{"CI_COMMIT_SHA"}},
		{"after the body", "cat <<'EOT'\n$CI_JOB_ID\nEOT\necho $CI_COMMIT_SHA", []string{"CI_COMMIT_SHA"}},
		{"tab stripped", "cat <<-'EOT'\n\t$CI_JOB_ID\n\tEOT\necho $CI_COMMIT_SHA", []string{"CI_COMMIT_SHA"}},
		{"here string", `read x <<< "$CI_COMMIT_SHA"`, []string{"CI_COMMIT_SHA"}},
		{"here string then text", "read x <<< \"$A\"\necho $CI_COMMIT_SHA", []string{"A", "CI_COMMIT_SHA"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := vars.Refs(c.text, true); !slices.Equal(got, c.want) {
				t.Errorf("Refs(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

func TestPrepareDefaultAndErrorFormsGetNoGuard(t *testing.T) {
	for _, line := range []string{`echo ${CI_JOB_ID:-none}`, `echo ${CI_JOB_ID-none}`} {
		got := prepared(t, line, provider.Drone)
		if strings.Contains(got, "pipemaster: lost") {
			t.Errorf("%q got a guard: %q", line, got)
		}
	}
	if got := prepared(t, `echo ${CI_PROJECT_DIR:-d}`, provider.Drone); !strings.Contains(got, "export CI_PROJECT_DIR=") {
		t.Errorf("a name with a default is still translated: %q", got)
	}
	if got := prepared(t, `echo $CI_JOB_ID ${CI_JOB_ID:-none}`, provider.Drone); !strings.Contains(got, "pipemaster: lost") {
		t.Errorf("a bare read next to a default keeps the guard: %q", got)
	}
}

func TestPrepareStopsOnPullRequestWhereTheValueDiffers(t *testing.T) {
	in := shellStep(`echo $GITHUB_REF_NAME`)
	in.Origin.Provider = provider.GHA
	p, _ := vars.Prepare(in, provider.GitLab)
	got := p.Pipelines[0].Jobs[0].Steps[0].Script[0]
	for _, want := range []string{"case ${CI_PIPELINE_SOURCE} in merge_request_event)", "exit 1;; esac", "on pull request events"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestPrepareStopsStepsWhoseImageNeedsALostName(t *testing.T) {
	in := shellStep(`echo hi`)
	in.Pipelines[0].Jobs[0].Container = &ir.Image{Ref: "reg/$CI_REGISTRY_IMAGE:v1"}
	p, res := vars.Prepare(in, provider.Drone)
	got := p.Pipelines[0].Jobs[0].Steps[0].Script[0]
	if !strings.Contains(got, "pipemaster: lost: job.container variable CI_REGISTRY_IMAGE has no drone source") || !strings.Contains(got, "exit 1") {
		t.Errorf("the step runs with a lost image: %q", got)
	}
	if res == nil {
		t.Fatal("no result")
	}
}
