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
