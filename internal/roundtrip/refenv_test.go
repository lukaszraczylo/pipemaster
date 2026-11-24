package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

const injectedBranch = `a";touch pwn;"`

func gitlabScriptProject(script string) *ir.Project {
	st := &ir.Step{ID: "s", Kind: ir.StepScript, Script: []string{script}}
	pl := &ir.Pipeline{ID: "p", Jobs: []*ir.Job{{ID: "j", Steps: []*ir.Step{st}}}}
	return &ir.Project{Origin: ir.Origin{Provider: provider.GitLab, Dialect: provider.GitLab}, Pipelines: []*ir.Pipeline{pl}}
}

// ghaRunStep decodes the first run step of the only job of an emitted GHA workflow.
func ghaRunStep(t *testing.T, files map[string][]byte) map[string]any {
	t.Helper()
	for n, b := range files {
		if !strings.HasPrefix(n, ".github/workflows/") {
			continue
		}
		var doc struct {
			Jobs map[string]struct {
				Steps []map[string]any `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		for _, j := range doc.Jobs {
			for _, st := range j.Steps {
				if _, ok := st["run"]; ok {
					return st
				}
			}
		}
	}
	t.Fatal("no run step emitted")
	return nil
}

// A branch name reaches the shell as data: GHA never pastes it into the script text.
func TestGHAScriptNeverHoldsAContributorChosenExpression(t *testing.T) {
	for _, script := range []string{
		`echo ${{ ref_name }}`,
		`echo "${{ ref_name }}"`,
		`echo '${{ ref_name }}'`,
		`echo "a ${{ ref_name }} b ${{ pr.head_ref }}"`,
	} {
		t.Run(script, func(t *testing.T) {
			proj := gitlabScriptProject(script)
			files, _, err := prov(t, provider.GHA).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			st := ghaRunStep(t, files)
			run, _ := st["run"].(string)
			if strings.Contains(run, "${{") {
				t.Fatalf("the script holds an expression:\n%s", run)
			}
			env, _ := st["env"].(map[string]any)
			if got, _ := env["PIPEMASTER_REF_1"].(string); got != "${{ github.ref_name }}" {
				t.Errorf("env = %v", env)
			}
			dir := t.TempDir()
			var vars []string
			for k, v := range env {
				s, _ := v.(string)
				switch s {
				case "${{ github.ref_name }}", "${{ github.head_ref }}":
					s = injectedBranch
				}
				vars = append(vars, k+"="+s)
			}
			out, errOut, code := runWithEnv(t, "cd '"+dir+"'\n"+run, vars)
			if code != 0 {
				t.Fatalf("script failed %d: %s", code, errOut)
			}
			if _, err := os.Stat(filepath.Join(dir, "pwn")); err == nil {
				t.Fatalf("the branch name ran as code; output %q", out)
			}
			if !strings.Contains(out, injectedBranch) && !strings.Contains(script, "'") {
				t.Errorf("the script lost the value: %q", out)
			}
			back, _, err := prov(t, provider.GHA).Parse(toFS(files))
			if err != nil {
				t.Fatal(err)
			}
			got := back.Pipelines[0].Jobs[0].Steps[0]
			if len(got.Env) != 0 || strings.Contains(strings.Join(got.Script, "\n"), "PIPEMASTER_REF_") {
				t.Errorf("the return parse keeps the binding: %q %v", got.Script, got.Env)
			}
		})
	}
}

// Per event, the derived value on the target equals the value on the source: a name that differs on some event
// is spelled by the prelude from the target's event variable.
func TestPreludeDerivesBranchPerEvent(t *testing.T) {
	cases := []struct {
		name, to, line string
		want           string
		env            []string
	}{
		{"drone push", provider.Drone, "echo \"b=$CI_COMMIT_BRANCH\"", "b=main", []string{"DRONE_BUILD_EVENT=push", "DRONE_COMMIT_BRANCH=main"}},
		{"drone tag", provider.Drone, "echo \"b=$CI_COMMIT_BRANCH\"", "b=", []string{"DRONE_BUILD_EVENT=tag", "DRONE_TAG=v1", "DRONE_COMMIT_BRANCH=main"}},
		{"drone pull request", provider.Drone, "echo \"b=$CI_COMMIT_BRANCH\"", "b=", []string{"DRONE_BUILD_EVENT=pull_request", "DRONE_COMMIT_BRANCH=main"}},
		{"drone ref name on pull request", provider.Drone, "echo \"n=$CI_COMMIT_REF_NAME\"", "n=feat", []string{"DRONE_BUILD_EVENT=pull_request", "DRONE_SOURCE_BRANCH=feat", "DRONE_COMMIT_BRANCH=main"}},
		{"drone ref name on tag", provider.Drone, "echo \"n=$CI_COMMIT_REF_NAME\"", "n=v1", []string{"DRONE_BUILD_EVENT=tag", "DRONE_TAG=v1", "DRONE_COMMIT_BRANCH=main"}},
		{"woodpecker ref name on pull request", provider.Woodpecker, "echo \"n=$CI_COMMIT_REF_NAME\"", "n=feat", []string{"CI_PIPELINE_EVENT=pull_request", "CI_COMMIT_SOURCE_BRANCH=feat", "CI_COMMIT_BRANCH=main"}},
		{"woodpecker branch on push", provider.Woodpecker, "echo \"b=$CI_COMMIT_BRANCH\"", "b=main", []string{"CI_PIPELINE_EVENT=push", "CI_COMMIT_BRANCH=main"}},
		{"woodpecker branch on pull request", provider.Woodpecker, "echo \"b=$CI_COMMIT_BRANCH\"", "b=", []string{"CI_PIPELINE_EVENT=pull_request", "CI_COMMIT_BRANCH=main"}},
		{"woodpecker branch on tag", provider.Woodpecker, "echo \"b=$CI_COMMIT_BRANCH\"", "b=", []string{"CI_PIPELINE_EVENT=tag", "CI_COMMIT_TAG=v1", "CI_COMMIT_BRANCH=main"}},
		{"gha branch on push", provider.GHA, "echo \"b=$CI_COMMIT_BRANCH\"", "b=main", []string{"GITHUB_EVENT_NAME=push", "GITHUB_REF_TYPE=branch", "GITHUB_REF_NAME=main"}},
		{"gha branch on tag", provider.GHA, "echo \"b=$CI_COMMIT_BRANCH\"", "b=", []string{"GITHUB_EVENT_NAME=push", "GITHUB_REF_TYPE=tag", "GITHUB_REF_NAME=v1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files, _, err := prov(t, c.to).Emit(gitlabScriptProject(c.line))
			if err != nil {
				t.Fatal(err)
			}
			var script string
			for _, cand := range scriptCandidates(t, files) {
				if strings.Contains(cand, "PIPEMASTER_VARS") {
					script = strings.ReplaceAll(cand, "$$", "$")
				}
			}
			if script == "" {
				t.Fatalf("no prelude in %v", scriptCandidates(t, files))
			}
			out, errOut, code := runWithEnv(t, script, c.env)
			if code != 0 || strings.TrimSpace(out) != c.want {
				t.Errorf("out %q code %d stderr %q, want %q", out, code, errOut, c.want)
			}
		})
	}
}

// A quote that a line opens stays open on the next lines, so a name there is neither translated nor lost on the way back.
func TestMultiLineQuoteSurvivesTheRoundTrip(t *testing.T) {
	const glDoc = "build:\n  script:\n    - |\n      echo \"one $CI_COMMIT_SHA\"\n      msg=\"$CI_COMMIT_SHA inside multiline'\"\n      echo 'first\n      $CI_COMMIT_SHA literal\n      end'\n"
	for _, via := range []string{provider.GHA, provider.Woodpecker, provider.Drone} {
		t.Run(via, func(t *testing.T) {
			r := hops(t, toFS(map[string][]byte{".gitlab-ci.yml": []byte(glDoc)}), prov(t, provider.GitLab), prov(t, via), false, true)
			if same, diff := ir.Equal(r.src, r.back); !same {
				t.Fatalf("not exact via %s:\n%s", via, diff)
			}
			got := strings.Join(r.src.Pipelines[0].Jobs[0].Steps[0].Script, "\n")
			if !strings.Contains(got, "\n$CI_COMMIT_SHA literal\n") || !strings.Contains(got, "${{ sha }} inside") {
				t.Errorf("the quoted line was translated or the plain one was not:\n%s", got)
			}
		})
	}
}

// GHA expands an expression before the shell runs, so a reference inside a multi-line single quote leaves the quote.
func TestGHAReferenceInMultiLineQuoteIsExpandedOnGitLab(t *testing.T) {
	const wf = "name: ci\non: [push]\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          echo 'first\n          ${{ github.sha }} second\n          end'\n"
	proj, _, err := prov(t, provider.GHA).Parse(toFS(map[string][]byte{".github/workflows/ci.yml": []byte(wf)}))
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := prov(t, provider.GitLab).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(files[".gitlab-ci.yml"])
	if !strings.Contains(doc, `'"$CI_COMMIT_SHA"' second`) {
		t.Errorf("the reference stays inside the quote:\n%s", doc)
	}
	if strings.Contains(doc, "- echo 'first\n") {
		t.Errorf("the quoted lines are separate script items:\n%s", doc)
	}
}

const droneBranchImage = "kind: pipeline\nname: ci\nsteps:\n  - name: build\n    image: alpine:${DRONE_BRANCH}\n    commands:\n      - echo hi\n"

// DRONE_BRANCH is the target branch on a pull request. Each target spells that value in its own field syntax.
func TestFieldVariableUsesTheTargetFieldSyntax(t *testing.T) {
	for _, c := range []struct{ to, want string }{
		{provider.Woodpecker, "image: alpine:${CI_COMMIT_BRANCH}"},
		{provider.GitLab, "image: alpine:${CI_MERGE_REQUEST_TARGET_BRANCH_NAME}${CI_COMMIT_BRANCH}"},
		{provider.GHA, "github.event_name == 'pull_request' && github.base_ref || github.ref_name"},
	} {
		t.Run(c.to, func(t *testing.T) {
			files, rep := emitFiles(t, "drone", c.to, map[string]string{".drone.yml": droneBranchImage})
			var all strings.Builder
			for n, b := range files {
				if !strings.HasPrefix(n, ".pipemaster/") {
					all.Write(b)
				}
			}
			if !strings.Contains(all.String(), c.want) {
				t.Errorf("missing %q in\n%s", c.want, all.String())
			}
			if strings.Contains(all.String(), "pipemaster: lost") {
				t.Errorf("a step stops although the value maps:\n%s", all.String())
			}
			for _, e := range rep.Entries {
				if e.Level.String() == "lost" && strings.Contains(e.Detail, "DRONE_BRANCH") {
					t.Errorf("reported as lost: %+v", e)
				}
			}
		})
	}
}

// A pipeline that runs on push only has no pull request where the two values differ.
func TestPushOnlyPipelineMapsEventKeyedFieldsToTheBranch(t *testing.T) {
	const doc = "kind: pipeline\nname: ci\ntrigger:\n  event:\n    - push\nsteps:\n  - name: build\n    image: alpine:${DRONE_BRANCH}\n    commands:\n      - echo hi\n"
	files, _ := emitFiles(t, "drone", provider.Tekton, map[string]string{".drone.yml": doc})
	var all strings.Builder
	for n, b := range files {
		if !strings.HasPrefix(n, ".pipemaster/") {
			all.Write(b)
		}
	}
	if strings.Contains(all.String(), "pipemaster: lost") {
		t.Errorf("a push-only pipeline still loses the image variable:\n%s", all.String())
	}
}
