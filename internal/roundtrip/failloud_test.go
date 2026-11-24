package roundtrip_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/internal/roundtrip"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/provider/emitkit"
)

// failLoudTargets are the targets that write a failing script for work with no target form.
var failLoudTargets = []string{provider.GHA, "gitlab", "woodpecker", "drone", "tekton", "argo", "concourse"}

// scriptCandidates returns every script text of the emitted files: each string value, and each list of strings
// joined by newlines, because GitLab, Drone and Woodpecker write a script as a list of commands.
func scriptCandidates(t *testing.T, files map[string][]byte) []string {
	t.Helper()
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.ScalarNode:
			out = append(out, n.Value)
		case yaml.SequenceNode:
			var parts []string
			for _, c := range n.Content {
				if c.Kind == yaml.ScalarNode {
					parts = append(parts, c.Value)
				}
			}
			out = append(out, parts...)
			// A command list joins into one script, unless it is argv of a wrapper that reads the script from $1.
			if len(parts) == len(n.Content) && len(parts) > 1 && !strings.HasPrefix(parts[0], "-") && !strings.HasPrefix(parts[0], "if command") {
				out = append(out, strings.Join(parts, "\n"))
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	for name, b := range files {
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		dec := yaml.NewDecoder(bytes.NewReader(b))
		for {
			var doc yaml.Node
			if err := dec.Decode(&doc); err != nil {
				break
			}
			walk(&doc)
		}
	}
	return out
}

// runScript runs script under sh in an empty directory and returns its streams and exit code.
func runScript(t *testing.T, script string) (stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "step.sh")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", file) // #nosec G204 -- fixed binary, generated script
	cmd.Dir = dir
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run sh: %v", err)
	}
	return o.String(), e.String(), code
}

func hasFailLoud(script string) bool {
	return slices.ContainsFunc(strings.Split(script, "\n"), emitkit.IsFailLoudLine)
}

func oneJob(steps ...*ir.Step) *ir.Project {
	pl := &ir.Pipeline{ID: "p", Jobs: []*ir.Job{{ID: "j", Steps: steps}}}
	return &ir.Project{Origin: ir.Origin{Provider: "gha", Dialect: "gha"}, Pipelines: []*ir.Pipeline{pl}}
}

// noFormStep is a step that holds source work no target can run.
func noFormStep(to string) *ir.Step {
	if to == provider.GHA {
		return &ir.Step{ID: "s", Kind: ir.StepTask}
	}
	return &ir.Step{ID: "s", Kind: ir.StepAction, Action: &ir.ActionRef{Kind: ir.ActionRepo, Host: "https://github.com", Owner: "acme", Repo: "tool", Ref: "v1"}}
}

// A step with no target form must not become a step that passes: the emitted script exits 1 and says what was lost.
func TestLostStepEmitsFailingScript(t *testing.T) {
	for _, to := range failLoudTargets {
		if to == "tekton" {
			continue
		}
		t.Run(to, func(t *testing.T) {
			files, _, err := prov(t, to).Emit(oneJob(noFormStep(to)))
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, s := range scriptCandidates(t, files) {
				if !hasFailLoud(s) {
					continue
				}
				n++
				_, stderr, code := runScript(t, s)
				if code != 1 {
					t.Errorf("exit code = %d, want 1\n%s", code, s)
				}
				if !strings.HasPrefix(stderr, "pipemaster: lost: ") || !strings.Contains(stderr, "pipeline/p/job/j") {
					t.Errorf("stderr = %q", stderr)
				}
			}
			if n == 0 {
				t.Errorf("no failing script in:\n%v", names(files))
			}
		})
	}
}

// lostLine is a script line that no target can spell: an expression of the source dialect with no equivalent.
const lostLine = "echo ${{ hashFiles('go.sum') }}"

// A lost command inside a script that otherwise runs stops the script at that point: later lines do not run.
func TestLostCommandStopsTheScript(t *testing.T) {
	step := &ir.Step{ID: "s", Kind: ir.StepScript, Image: &ir.Image{Ref: "alpine:3"},
		Script: []string{"echo first", lostLine, "echo third"}}
	for _, to := range failLoudTargets {
		if to == provider.GHA {
			continue // the source dialect spells every expression
		}
		t.Run(to, func(t *testing.T) {
			files, _, err := prov(t, to).Emit(oneJob(step))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, s := range scriptCandidates(t, files) {
				if !hasFailLoud(s) || !strings.Contains(s, "echo first") {
					continue
				}
				found = true
				stdout, stderr, code := runScript(t, s)
				if code != 1 || stdout != "first\n" || !strings.Contains(stderr, "pipemaster: lost: script.interpolation") {
					t.Errorf("code=%d stdout=%q stderr=%q\n%s", code, stdout, stderr, s)
				}
			}
			if !found {
				t.Errorf("no script with a failing line; files %v", names(files))
			}
			all := ""
			for _, b := range files {
				all += string(b)
			}
			if !strings.Contains(all, "# pipemaster:lost") {
				t.Errorf("the lost comment is gone")
			}
		})
	}
}

// The return trip restores the source step, not the failing script that stood in for it.
func TestFailingScriptRestoresTheSourceStep(t *testing.T) {
	lostScript := &ir.Step{ID: "l", Kind: ir.StepScript, Image: &ir.Image{Ref: "alpine:3"}, Script: []string{"echo first", lostLine, "echo third"}}
	for _, to := range failLoudTargets {
		for name, mk := range map[string]func() *ir.Project{
			"no-form-step": func() *ir.Project { return oneJob(noFormStep(to)) },
			"lost-command": func() *ir.Project { return oneJob(lostScript) },
		} {
			if to == provider.GHA && name == "lost-command" {
				continue
			}
			t.Run(to+"/"+name, func(t *testing.T) {
				src, target := mk(), prov(t, to)
				files, rep, err := target.Emit(src)
				if err != nil {
					t.Fatal(err)
				}
				back, _, err := target.Parse(toFS(files))
				if err != nil {
					t.Fatal(err)
				}
				roundtrip.Restore(back, roundtrip.NewMeta("gha", src, target, rep))
				for _, d := range diffNodes(mk(), back) {
					if to == "concourse" && d.Field == "Ext" {
						continue // the parser keeps resources and task inputs in Ext, which the stored layers hold
					}
					t.Errorf("A->B->A: %s differs at %s", d.Field, d.Path)
				}
			})
		}
	}
}
