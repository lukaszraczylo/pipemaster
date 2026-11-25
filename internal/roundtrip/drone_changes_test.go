package roundtrip_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"

	"github.com/lukaszraczylo/pipemaster/provider"
)

type droneStep struct {
	Env      map[string]string `yaml:"environment"`
	Name     string            `yaml:"name"`
	Image    string            `yaml:"image"`
	Commands []string          `yaml:"commands"`
}

type droneDoc struct {
	Trigger struct {
		Event any `yaml:"event"`
	} `yaml:"trigger"`
	Steps []droneStep `yaml:"steps"`
}

// droneEnv simulates the Drone variables of a scenario.
func droneEnv(s scenario, before, after string) []string {
	env := []string{"DRONE_COMMIT_BEFORE=" + before, "DRONE_COMMIT_AFTER=" + after, "DRONE_COMMIT_SHA=" + after, "DRONE_BRANCH=main", "DRONE_TARGET_BRANCH=main"}
	switch {
	case s.pr:
		env = append(env, "DRONE_BUILD_EVENT=pull_request", "DRONE_TARGET_BRANCH=target")
	case s.sched:
		env = append(env, "DRONE_BUILD_EVENT=cron")
	case s.tag:
		env = append(env, "DRONE_BUILD_EVENT=tag", "DRONE_TAG=v1", "DRONE_BRANCH=", "DRONE_TARGET_BRANCH=")
	default:
		env = append(env, "DRONE_BUILD_EVENT=push", "DRONE_BRANCH="+s.gh["ref_name"].(string))
		if s.created {
			env[0] = "DRONE_COMMIT_BEFORE=" + zeroSHA
		}
	}
	return env
}

// shell runs a script with sh in dir under the environment and returns its output and error.
func shell(dir, script string, env []string) (string, error) {
	cmd := exec.Command("sh", "-c", script) //nolint:gosec // generated script under test, temp repository
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func unesc(s string) string { return strings.ReplaceAll(s, "$$", "$") }

// TestGitlabChangesRunOnDroneExactlyWhereGitLabRuns converts rules with path predicates to Drone, runs the generated
// step and the guard of the next step in a real repository for each event and each change set, and compares the
// outcome with the GitLab model. A step may never run where GitLab skips the job.
func TestGitlabChangesRunOnDroneExactlyWhereGitLabRuns(t *testing.T) {
	forms := []struct {
		name  string
		rules []arm
	}{
		{"changes only", []arm{{Changes: goGlobs}}},
		{"branch and changes", []arm{{If: onMain, Changes: goGlobs}}},
		{"changes or changes", []arm{{Changes: goGlobs}, {Changes: []string{"Makefile"}}}},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			src := "j:\n  script: [echo hi]\n  rules:\n" + armsYAML(f.rules)
			out, rep := convert(t, provider.GitLab, "drone", gitlabCI, src)
			for _, e := range rep.Entries {
				if e.Feature == caps.JobIf && e.Level == caps.Lost {
					t.Fatalf("the condition is lost: %s", e.Detail)
				}
			}
			var doc droneDoc
			if err := yaml.Unmarshal([]byte(sections(out)[".drone.yml"]), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Steps) != 2 || doc.Steps[0].Name != "pipemaster-changes" {
				t.Fatalf("want the generated step first and the job step second:\n%s", out)
			}
			gen, job := doc.Steps[0], doc.Steps[1]
			events := anyList(doc.Trigger.Event)
			ran := 0
			for _, s := range scenarios {
				if s.deleted {
					continue
				}
				for set, files := range map[string]map[string]string{"README.md only": {"README.md": "b"}, "go file": {"src/a.go": "package a"}, "Makefile": {"Makefile": "x:"}} {
					dir, before, after := history(t, files)
					git(t, dir, "update-ref", "refs/heads/target", before)
					git(t, dir, "remote", "add", "origin", dir)
					env := droneEnv(s, before, after)
					for k, v := range gen.Env {
						env = append(env, k+"="+unesc(v))
					}
					if text, err := shell(dir, unesc(gen.Commands[0]), env); err != nil {
						t.Fatalf("%s / %s: the generated step failed: %v\n%s", s.name, set, err, text)
					}
					guardOut, err := shell(dir, unesc(job.Commands[0])+"\necho RAN", env)
					if err != nil {
						t.Fatalf("%s / %s: guard: %v\n%s", s.name, set, err, guardOut)
					}
					admitted := len(events) == 0 || containsAny(events, droneEventOf(s))
					got := admitted && strings.Contains(guardOut, "RAN")
					_, goChanged := files["src/a.go"]
					_, mkChanged := files["Makefile"]
					want := glDecision(t, s, nil, f.rules, goChanged || (mkChanged && len(f.rules) > 1))
					if got && !want {
						t.Errorf("%s / %s: Drone runs, GitLab skips", s.name, set)
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

func containsAny(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
