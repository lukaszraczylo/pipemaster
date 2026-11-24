package roundtrip_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr/ctxparam"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
	"github.com/lukaszraczylo/pipemaster/vars"
)

var (
	varSources = []string{provider.GitLab, provider.Drone, provider.Woodpecker, provider.GHA}
	varTargets = []string{provider.GHA, provider.GitLab, provider.Drone, provider.Woodpecker, provider.Tekton, provider.Argo, provider.Concourse}
	// tektonParamRE and argoParamRE match the reserved context parameters that those targets substitute.
	tektonParamRE = regexp.MustCompile(`\$\(params\.(ctx-[a-z-]+)\)`)
	argoParamRE   = regexp.MustCompile(`\{\{workflow\.parameters\.(ctx-[a-z-]+)\}\}`)
)

func varsTable(t *testing.T) *vars.Table {
	t.Helper()
	tab, err := vars.Load()
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// echoProject has one step per variable of the source: it prints the value that the source name holds.
func echoProject(tab *vars.Table, src string) (*ir.Project, map[string]string) {
	var steps []*ir.Step
	nameOf := map[string]string{}
	for _, id := range tab.IDs() {
		ns := tab.Names(id, src)
		if len(ns) == 0 {
			continue
		}
		nameOf[id] = ns[0]
		steps = append(steps, &ir.Step{ID: "s-" + strings.ReplaceAll(id, "_", "-"), Kind: ir.StepScript, Script: []string{fmt.Sprintf(`echo "MARK_%s=$%s"`, id, ns[0])}})
	}
	pl := &ir.Pipeline{ID: "p", Jobs: []*ir.Job{{ID: "j", Steps: steps}}}
	return &ir.Project{Origin: ir.Origin{Provider: src, Dialect: src}, Pipelines: []*ir.Pipeline{pl}}, nameOf
}

// simulate returns the environment that target gives a script, and the values of the reserved parameters that
// Tekton and Argo substitute. Each entry has the value VAL_<id>.
func simulate(tab *vars.Table, target string) (env []string, params map[string]string) {
	params = map[string]string{}
	for _, id := range tab.IDs() {
		for _, n := range tab.Names(id, target) {
			env = append(env, n+"=VAL_"+id)
		}
		if c := tab.Canon(id); c != "" {
			if p, ok := ctxparam.Param(strings.Split(c, ".")); ok {
				params[p] = "VAL_" + id
				// Argo and Tekton may bind a parameter to a PM_CTX_ variable of the step instead.
				env = append(env, "PM_CTX_"+strings.ToUpper(strings.ReplaceAll(strings.TrimPrefix(p, ctxparam.Prefix), "-", "_"))+"=VAL_"+id)
			}
		}
	}
	return env, params
}

func runWithEnv(t *testing.T, script string, env []string) (out, errOut string, code int) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "step.sh")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", file) // #nosec G204 -- fixed binary, generated script
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run sh: %v", err)
		}
		code = ee.ExitCode()
	}
	return o.String(), e.String(), code
}

// runnable turns an emitted script candidate into text that sh runs: Drone and Woodpecker double the dollar,
// and Tekton and Argo substitute their parameters before the script runs.
func runnable(c string, params map[string]string) string {
	c = strings.ReplaceAll(c, "$$", "$")
	sub := func(re *regexp.Regexp) {
		c = re.ReplaceAllStringFunc(c, func(m string) string { return params[re.FindStringSubmatch(m)[1]] })
	}
	sub(tektonParamRE)
	sub(argoParamRE)
	return c
}

// Per source and target, a script that echoes each mapped variable prints the target's value, or stops with
// the lost message when the target has no source for it.
func TestPredefinedVariablesTranslateOnEveryPair(t *testing.T) {
	tab := varsTable(t)
	for _, src := range varSources {
		for _, to := range varTargets {
			if src == to {
				continue
			}
			t.Run(src+"-"+to, func(t *testing.T) {
				proj, nameOf := echoProject(tab, src)
				files, _, err := prov(t, to).Emit(proj)
				if err != nil {
					t.Fatal(err)
				}
				env, params := simulate(tab, to)
				cands := scriptCandidates(t, files)
				for id, name := range nameOf {
					var script string
					for _, c := range cands {
						if strings.Contains(c, "MARK_"+id+"=") {
							script = runnable(c, params)
							break
						}
					}
					if script == "" {
						t.Errorf("%s: no emitted script for %s", id, name)
						continue
					}
					out, errOut, code := runWithEnv(t, script, env)
					switch {
					case len(tab.Names(id, to)) > 0 || templatedOn(tab, id, to):
						if want := "MARK_" + id + "=VAL_" + id; !strings.Contains(out, want) {
							t.Errorf("%s (%s): output %q lacks %q; script %q", id, name, out, want, script)
						}
					case tab.Derives(id, to):
						if code != 0 {
							t.Errorf("%s (%s): derived value failed with %d: %s", id, name, code, errOut)
						}
					default:
						if code == 0 || !strings.Contains(errOut, "variable "+name+" has no "+to+" source") {
							t.Errorf("%s (%s): want the lost message, got code %d, stderr %q", id, name, code, errOut)
						}
					}
				}
			})
		}
	}
}

func templatedOn(tab *vars.Table, id, target string) bool {
	if target != provider.Tekton && target != provider.Argo {
		return false
	}
	_, ok := ctxparam.Param(strings.Split(tab.Canon(id), "."))
	return ok && tab.Canon(id) != ""
}

// A script and its return trip must match: the prelude and the guard are stripped on parse.
func TestPreludeIsStrippedOnParse(t *testing.T) {
	tab := varsTable(t)
	for _, to := range varTargets {
		t.Run(to, func(t *testing.T) {
			proj, _ := echoProject(tab, provider.GitLab)
			want := map[string]string{}
			for _, st := range proj.Pipelines[0].Jobs[0].Steps {
				want[st.ID] = st.Script[0]
			}
			files, _, err := prov(t, to).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			back, _, err := prov(t, to).Parse(toFS(files))
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, pl := range back.Pipelines {
				for _, j := range pl.Jobs {
					for _, st := range j.Steps {
						for _, line := range st.Script {
							if strings.Contains(line, "PIPEMASTER_VARS=") {
								t.Errorf("step %s keeps the prelude: %q", st.ID, line)
							}
						}
						n++
					}
				}
			}
			if n == 0 {
				t.Fatal("parsed no steps")
			}
		})
	}
}

// A reference in single quotes is not a use: the shell never expands it.
func TestSingleQuotedReferenceGetsNoPrelude(t *testing.T) {
	for _, to := range varTargets {
		proj := &ir.Project{Origin: ir.Origin{Provider: provider.GitLab}, Pipelines: []*ir.Pipeline{{ID: "p", Jobs: []*ir.Job{{ID: "j",
			Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Image: &ir.Image{Ref: "alpine:3"}, Script: []string{`echo '$CI_JOB_ID'`}}}}}}}}
		files, _, err := prov(t, to).Emit(proj)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range scriptCandidates(t, files) {
			if strings.Contains(c, "PIPEMASTER_VARS=") {
				t.Errorf("%s: single-quoted reference got a prelude: %q", to, c)
			}
		}
	}
}

var gitlabRefRE = regexp.MustCompile(`\$\$?\{?((?:CI|GITLAB)_[A-Z0-9_]+)`)

// Corpus 21 holds many GitLab variables. Each one that reaches a non-GitLab file is either native on the target,
// or defined by a prelude, or guarded.
func TestCorpusGitLabShellHasNoUntranslatedVariables(t *testing.T) {
	tab := varsTable(t)
	for _, to := range varTargets {
		if to == provider.GitLab {
			continue
		}
		t.Run(to, func(t *testing.T) {
			proj, _, err := prov(t, provider.GitLab).Parse(os.DirFS("../../testdata/corpus/21-gitlab-gitlab-shell"))
			if err != nil {
				t.Fatal(err)
			}
			need := readNames(proj)
			files, _, err := prov(t, to).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			for name, b := range files {
				if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
					continue
				}
				text := string(b)
				if onlyNoop(text) {
					continue
				}
				for _, line := range strings.Split(text, "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), "#") {
						continue
					}
					for _, m := range gitlabRefRE.FindAllStringSubmatch(line, -1) {
						n := m[1]
						if tab.Native(n, provider.GitLab, to) || !need[n] {
							continue
						}
						if !strings.Contains(text, "export "+n+"=") && !strings.Contains(text, "${"+n+":?") {
							t.Errorf("%s: $%s has no prelude and no guard: %s", name, n, strings.TrimSpace(line))
						}
					}
				}
			}
		})
	}
}

// A GHA script reads GITHUB_* variables and ${{ github.* }} expressions. Both reach every target.
func TestGHASourceVariablesTranslate(t *testing.T) {
	const wf = `name: ci
on: [push]
jobs:
  vars:
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo "branch:$GITHUB_REF_NAME sha:$GITHUB_SHA"
          echo "expr:${{ github.sha }} ${{ github.ref_name }}"
`
	want := map[string][]string{
		provider.Drone:      {"DRONE_COMMIT_SHA", "DRONE_COMMIT_BRANCH"},
		provider.Woodpecker: {"CI_COMMIT_SHA", "CI_COMMIT_BRANCH"},
		provider.GitLab:     {"CI_COMMIT_SHA", "CI_COMMIT_REF_NAME"},
	}
	fsys := toFS(map[string][]byte{".github/workflows/ci.yml": []byte(wf)})
	for _, to := range []string{provider.Drone, provider.Woodpecker, provider.GitLab, provider.Tekton, provider.Argo} {
		t.Run(to, func(t *testing.T) {
			proj, _, err := prov(t, provider.GHA).Parse(fsys)
			if err != nil {
				t.Fatal(err)
			}
			files, _, err := prov(t, to).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			var all strings.Builder
			for _, c := range scriptCandidates(t, files) {
				all.WriteString(c + "\n")
			}
			text := all.String()
			if !strings.Contains(text, "GITHUB_SHA=") || !strings.Contains(text, "GITHUB_REF_NAME=") {
				t.Errorf("no prelude for $GITHUB_SHA and $GITHUB_REF_NAME:\n%s", text)
			}
			for _, w := range want[to] {
				if !strings.Contains(text, w) {
					t.Errorf("missing %s:\n%s", w, text)
				}
			}
		})
	}
}

// GHA runs a script under bash when the step names no shell and bash exists, and a GitLab bash runner does the
// same. The GHA file says so, and the return trip drops the line again.
func TestGitLabBashScriptRunsUnderBashOnGHA(t *testing.T) {
	const gl = "build:\n  script:\n    - arr=(a b c)\n    - if [[ ${#arr[@]} -eq 3 ]]; then echo ok; fi\n"
	proj, _, err := prov(t, provider.GitLab).Parse(toFS(map[string][]byte{".gitlab-ci.yml": []byte(gl)}))
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := prov(t, provider.GHA).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, b := range files {
		text += string(b)
	}
	if !strings.Contains(text, "shell: bash") {
		t.Fatalf("no shell: bash in\n%s", text)
	}
	back, _, err := prov(t, provider.GHA).Parse(toFS(files))
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range back.Pipelines[0].Jobs {
		for _, st := range j.Steps {
			if st.Shell != "" {
				t.Errorf("step %s keeps shell %q after the return parse", st.ID, st.Shell)
			}
		}
	}
}

// A Tekton script that names bash needs a shebang with a path: the entrypoint execs the script file.
func TestTektonShellShebangHasAPath(t *testing.T) {
	const wf = "name: ci\non: [push]\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - shell: bash\n        run: |\n          arr=(a b c)\n          echo \"${arr[1]}\"\n"
	proj, _, err := prov(t, provider.GHA).Parse(toFS(map[string][]byte{".github/workflows/ci.yml": []byte(wf)}))
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := prov(t, provider.Tekton).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	var script string
	for _, c := range scriptCandidates(t, files) {
		if strings.Contains(c, "arr=(a b c)") {
			script = c
		}
	}
	if !strings.HasPrefix(script, "#!/usr/bin/env bash\n") {
		t.Fatalf("script starts %q", script)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	file := filepath.Join(t.TempDir(), "script-0")
	if err := os.WriteFile(file, []byte(script), 0o700); err != nil { // #nosec G306 -- test script must be executable
		t.Fatal(err)
	}
	out, err := exec.Command(file).CombinedOutput() // #nosec G204 -- generated test script
	if err != nil || strings.TrimSpace(string(out)) != "b" {
		t.Fatalf("exec like the Tekton entrypoint: %q, %v", out, err)
	}
	back, _, err := prov(t, provider.Tekton).Parse(toFS(files))
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Pipelines[0].Jobs[0].Steps[0].Shell; got != "bash" {
		t.Errorf("shell after the return parse = %q", got)
	}
}

// readNames lists the predefined names that a runnable script step can read: through its script, or through
// the variable values of its job and pipeline. A job whose only step is a noop or a failing line reads nothing.
func readNames(p *ir.Project) map[string]bool {
	out := map[string]bool{}
	for _, pl := range p.Pipelines {
		for _, j := range pl.Jobs {
			runs := false
			for _, st := range j.Steps {
				if len(st.Script) == 0 || emitkit.IsFailLoud(st.Script) || (len(st.Script) == 1 && st.Script[0] == "true") {
					continue
				}
				runs = true
				for _, n := range vars.Refs(strings.Join(st.Script, "\n"), true) {
					out[n] = true
				}
			}
			if !runs {
				continue
			}
			for _, v := range append(append([]ir.Var(nil), pl.Env...), j.Env...) {
				for _, n := range vars.Refs(v.Value.Lit, false) {
					out[n] = true
				}
			}
		}
	}
	return out
}

var scriptKeyRE = regexp.MustCompile(`(?m)^\s*script: (.*)$`)

// onlyNoop reports whether every script key of a per-job file is the noop placeholder, so nothing reads a variable.
func onlyNoop(text string) bool {
	m := scriptKeyRE.FindAllStringSubmatch(text, -1)
	for _, x := range m {
		if x[1] != `"true"` {
			return false
		}
	}
	return len(m) > 0
}

// dashImages are the names of one image, whose sh is dash and which has bash; the second is a mirror for
// hosts that hit the pull limit of the first.
var dashImages = []string{"debian:bookworm-slim", "mirror.gcr.io/library/debian:bookworm-slim"}

// dockerRun runs a shell command line in the dash image and skips the test when docker is not there.
func dockerRun(t *testing.T, line string) (string, error) {
	t.Helper()
	return dockerRunIn(t, dashImages, line)
}

// noBashImages are the names of one image that has no bash.
var noBashImages = []string{"alpine:3", "mirror.gcr.io/library/alpine:3"}

// dockerRunIn runs a shell command line in the first of images that docker has or pulls.
func dockerRunIn(t *testing.T, images []string, line string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker")
	}
	cfg := t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "DOCKER_CONFIG="+cfg)
	if os.Getenv("DOCKER_HOST") == "" {
		env = append(env, "DOCKER_HOST=unix://"+os.Getenv("HOME")+"/.kiri/run/docker.sock")
	}
	probe := exec.Command("docker", "version", "--format", "{{.Server.Version}}") // #nosec G204 -- fixed arguments
	probe.Env = env
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("docker is not available: %s", out)
	}
	for _, img := range images {
		pull := exec.Command("docker", "image", "inspect", img) // #nosec G204 -- fixed arguments
		pull.Env = env
		if pull.Run() != nil {
			pull = exec.Command("docker", "pull", "-q", img) // #nosec G204 -- fixed arguments
			pull.Env = env
			if pull.Run() != nil {
				continue
			}
		}
		cmd := exec.Command("docker", "run", "--rm", img, "sh", "-c", line) // #nosec G204 -- generated test command
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Skip("no dash image and no way to pull one")
	return "", nil
}

// A bash-only GHA script reaches Drone and Woodpecker as one command that the image sh runs: the wrapper finds
// bash in the image and re-runs the script under it.
func TestBashOnlyScriptRunsUnderDashOnDroneAndWoodpecker(t *testing.T) {
	const wf = "name: ci\non: [push]\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          arr=(a b c)\n          if [[ ${#arr[@]} -eq 3 && ${arr[1]} == b ]]; then echo \"MARK bash.ok:${arr[2]}\"; fi\n"
	for _, to := range []string{provider.Drone, provider.Woodpecker} {
		t.Run(to, func(t *testing.T) {
			proj, _, err := prov(t, provider.GHA).Parse(toFS(map[string][]byte{".github/workflows/ci.yml": []byte(wf)}))
			if err != nil {
				t.Fatal(err)
			}
			files, rep, err := prov(t, to).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			var cmd string
			for _, c := range scriptCandidates(t, files) {
				if strings.HasPrefix(c, "sh -c ") {
					cmd = strings.ReplaceAll(c, "$$", "$")
				}
			}
			if cmd == "" {
				t.Fatalf("no wrapper command in %v", scriptCandidates(t, files))
			}
			emulated := false
			for _, e := range rep.Entries {
				emulated = emulated || (e.Feature == "step.shell" && e.Level.String() == "emulated")
			}
			if !emulated {
				t.Errorf("the wrapper is not reported as emulated: %+v", rep.Entries)
			}
			out, err := dockerRun(t, cmd)
			if err != nil || !strings.Contains(out, "MARK bash.ok:c") {
				t.Fatalf("run in the dash image: %v\n%s", err, out)
			}
			back, _, err := prov(t, to).Parse(toFS(files))
			if err != nil {
				t.Fatal(err)
			}
			st := back.Pipelines[0].Jobs[0].Steps[0]
			if len(st.Script) != 2 || !strings.HasPrefix(st.Script[0], "arr=(a b c)") || st.Shell != "" {
				t.Errorf("the return parse keeps the wrapper: %q shell %q", st.Script, st.Shell)
			}
		})
	}
}

// Command and args become one script line. Each argument is one shell word, so a space, a quote or a dollar in an
// argument reaches the program unchanged.
func TestCommandArgsKeepEachArgumentAsOneWord(t *testing.T) {
	const task = `apiVersion: tekton.dev/v1
kind: Pipeline
metadata:
  name: p
spec:
  tasks:
    - name: t
      taskSpec:
        steps:
          - name: s
            image: alpine:3
            command: [printf]
            args: ["[%s]", "a b", "it's", "$HOME", ""]
`
	for _, to := range []string{provider.GitLab, provider.Drone, provider.Woodpecker, provider.Concourse} {
		t.Run(to, func(t *testing.T) {
			proj, _, err := prov(t, provider.Tekton).Parse(toFS(map[string][]byte{".tekton/task.yaml": []byte(task)}))
			if err != nil {
				t.Fatal(err)
			}
			files, _, err := prov(t, to).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			var script string
			for _, c := range scriptCandidates(t, files) {
				if strings.Contains(c, "printf") && strings.Contains(c, "a b") {
					script = strings.ReplaceAll(c, "$$", "$")
				}
			}
			if script == "" {
				t.Fatalf("no script in %v", scriptCandidates(t, files))
			}
			out, errOut, code := runWithEnv(t, script, []string{"HOME=/h"})
			if code != 0 || out != "[a b][it's][$HOME][]" {
				t.Errorf("printf printed %q (code %d, %s) from %q", out, code, errOut, script)
			}
		})
	}
}

// unwrapBashCommand returns the script that a bash-if-present wrapper command runs, or cmd when it is no wrapper.
func unwrapBashCommand(cmd string) string {
	for _, wrapper := range []string{provider.BashOrShScript, strings.ReplaceAll(provider.BashOrShScript, "$", "$$")} {
		if inner, ok := provider.BashUnwrapCommand(wrapper, cmd); ok {
			return inner
		}
	}
	return cmd
}

// An image without bash runs the wrapped script under sh: the wrapper never fails on a missing bash.
func TestWrappedScriptRunsOnAnImageWithoutBash(t *testing.T) {
	const wf = "name: ci\non: [push]\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          echo \"MARK posix.ok:$((1+2))\"\n"
	for _, to := range []string{provider.Drone, provider.Woodpecker} {
		t.Run(to, func(t *testing.T) {
			proj, _, err := prov(t, provider.GHA).Parse(toFS(map[string][]byte{".github/workflows/ci.yml": []byte(wf)}))
			if err != nil {
				t.Fatal(err)
			}
			files, _, err := prov(t, to).Emit(proj)
			if err != nil {
				t.Fatal(err)
			}
			var cmd string
			for _, c := range scriptCandidates(t, files) {
				if strings.HasPrefix(c, "sh -c ") {
					cmd = strings.ReplaceAll(c, "$$", "$")
				}
			}
			if cmd == "" {
				t.Fatalf("no wrapper command in %v", scriptCandidates(t, files))
			}
			out, err := dockerRunIn(t, noBashImages, cmd)
			if err != nil || !strings.Contains(out, "MARK posix.ok:3") {
				t.Fatalf("run in the image without bash: %v\n%s", err, out)
			}
		})
	}
}
