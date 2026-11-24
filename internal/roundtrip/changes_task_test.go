package roundtrip_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider"
)

// generatedChanges holds the script and env of the generated changes task of a converted pipeline.
type generatedChanges struct {
	env    map[string]string
	script string
	gated  string
}

func sections(out string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(out, "### ")[1:] {
		name, body, _ := strings.Cut(part, "\n")
		m[name] = body
	}
	return m
}

func envMap(t *testing.T, list any) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, e := range list.([]any) {
		m := e.(map[string]any)
		env[m["name"].(string)], _ = m["value"].(string)
	}
	return env
}

// extractChanges reads the generated task out of a converted pipeline; the gated text is the pipeline file.
func extractChanges(t *testing.T, target, out string) generatedChanges {
	t.Helper()
	var g generatedChanges
	for name, body := range sections(out) {
		switch {
		case target == provider.Tekton && strings.HasSuffix(name, "-pipemaster-changes.yaml"):
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}
			step := doc["spec"].(map[string]any)["steps"].([]any)[0].(map[string]any)
			g.script, g.env = step["script"].(string), envMap(t, step["env"])
		case target == provider.Tekton && name == ".tekton/pipeline.yaml":
			g.gated = body
		case target == provider.Argo && strings.HasPrefix(name, ".argo/workflows/"):
			g.gated = body
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatal(err)
			}
			for _, tm := range doc["spec"].(map[string]any)["templates"].([]any) {
				m := tm.(map[string]any)
				if m["name"] == "pipemaster-changes" {
					c := m["container"].(map[string]any)
					g.script, g.env = c["args"].([]any)[0].(string), envMap(t, c["env"])
				}
			}
		}
	}
	if g.script == "" {
		t.Fatalf("no generated changes task in the %s output:\n%s", target, out)
	}
	return g
}

var (
	tektonResult = regexp.MustCompile(`\$\(results\.([A-Za-z0-9_-]+)\.path\)`)
	argoParam    = regexp.MustCompile(`\{\{workflow\.parameters\.([a-z-]+)\}\}`)
)

// run executes the generated script for one event in a copy of the repository and returns each result.
func (g generatedChanges) run(t *testing.T, target string, s scenario, files map[string]string) map[string]string {
	t.Helper()
	dir, before, after := history(t, files)
	resDir := t.TempDir()
	script := g.script
	var names []string
	if target == provider.Tekton {
		for _, m := range tektonResult.FindAllStringSubmatch(script, -1) {
			names = append(names, m[1])
		}
		script = tektonResult.ReplaceAllString(script, resDir+"/$1")
	} else {
		script = strings.ReplaceAll(script, "/tmp/pipemaster-out", resDir)
		for _, m := range regexp.MustCompile(resDir+`/(s[0-9a-f]+)`).FindAllStringSubmatch(script, -1) {
			names = append(names, m[1])
		}
	}
	script = strings.ReplaceAll(script, "/tmp/pipemaster-changes.sh", filepath.Join(resDir, "changes.sh"))
	params := map[string]string{"ctx-event": "push", "ctx-ref-type": "branch", "ctx-sha": after, "ctx-before-sha": before}
	switch {
	case s.pr:
		params["ctx-event"], params["ctx-pr-base-sha"] = "pull_request", before
	case s.sched:
		params["ctx-event"] = "schedule"
	case s.tag:
		params["ctx-ref-type"] = "tag"
	case s.created:
		params["ctx-before-sha"] = zeroSHA
	}
	cmd := exec.Command("sh", "-c", script) //nolint:gosec // generated script under test, temp repository
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url.file:///nonexistent/.insteadOf", "GIT_CONFIG_VALUE_0=https://github.com/")
	for k, v := range g.env {
		if m := tektonParam.FindStringSubmatch(v); m != nil {
			v = params[m[1]]
		} else if m := argoParam.FindStringSubmatch(v); m != nil {
			v = params[m[1]]
		}
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated script failed: %v\n%s", err, out)
	}
	res := map[string]string{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(resDir, n)) //nolint:gosec // a result file in the test temp dir
		if err != nil {
			t.Fatalf("result %s: %v", n, err)
		}
		res[n] = string(b)
	}
	return res
}

var tektonParam = regexp.MustCompile(`^\$\(params\.([a-z-]+)\)$`)

// TestChangesTaskRunsOnTektonAndArgoExactlyWhereGitLabRuns converts rules with path predicates, runs the generated
// script in a real repository for each event and each change set, and compares the verdict joined with the rest of
// the condition against the GitLab model. A job may never run where GitLab skips it.
func TestChangesTaskRunsOnTektonAndArgoExactlyWhereGitLabRuns(t *testing.T) {
	forms := []struct {
		name  string
		gate  string
		rules []arm
	}{
		{"changes only", "", []arm{{Changes: goGlobs}}},
		{"branch and changes", "ctx-branch", []arm{{If: onMain, Changes: goGlobs}}},
		{"changes or changes", "", []arm{{Changes: goGlobs}, {Changes: []string{"Makefile"}}}},
	}
	for _, target := range []string{provider.Tekton, provider.Argo} {
		for _, f := range forms {
			t.Run(target+"/"+f.name, func(t *testing.T) {
				src := "j:\n  script: [echo hi]\n  rules:\n" + armsYAML(f.rules)
				out, _ := convert(t, provider.GitLab, target, gitlabCI, src)
				g := extractChanges(t, target, out)
				if f.gate != "" && !strings.Contains(g.gated, f.gate) {
					t.Errorf("the branch test is not in the gated task:\n%s", g.gated)
				}
				ran := 0
				for _, s := range scenarios {
					if s.deleted {
						continue
					}
					for set, files := range map[string]map[string]string{"README.md only": {"README.md": "b"}, "go file": {"src/a.go": "package a"}, "Makefile": {"Makefile": "x:"}} {
						res := g.run(t, target, s, files)
						changed := false
						for _, v := range res {
							changed = changed || v == "true"
						}
						got := changed && glDecision(t, s, nil, f.rules, true)
						_, goChanged := files["src/a.go"]
						_, mkChanged := files["Makefile"]
						want := glDecision(t, s, nil, f.rules, goChanged || (mkChanged && len(f.rules) > 1))
						if got != want {
							t.Errorf("%s / %s: generated task runs=%v, GitLab runs=%v (results %v)", s.name, set, got, want, res)
						}
						if got {
							ran++
						}
					}
				}
				if ran == 0 {
					t.Error("the job never runs, so the comparison proves nothing")
				}
			})
		}
	}
}
