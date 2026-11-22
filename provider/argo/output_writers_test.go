package argo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

// runLastContainer runs the last container of the job template under sh with the workspace moved to a temp
// directory and the literal env entries set, and returns the file that the job output ver names.
func runLastContainer(t *testing.T, files map[string]string, extraEnv ...string) string {
	t.Helper()
	tm := named(t, get(template(t, files), "spec", "templates"), "build")
	cs := list(get(tm, "containerSet", "containers"))
	last := cs[len(cs)-1].(doc)
	args := list(last["args"])
	ws := t.TempDir()
	script := strings.ReplaceAll(args[0].(string), "/workspace", ws)
	env := append(os.Environ(), extraEnv...)
	for _, e := range list(last["env"]) {
		m := e.(doc)
		if v, _ := m["value"].(string); !strings.Contains(v, "{{") {
			env = append(env, m["name"].(string)+"="+v)
		}
	}
	cmd := exec.Command("sh", "-ec", script) // #nosec G204 -- the script is the emitter output under test
	cmd.Env = env
	cmd.Dir = ws
	_ = cmd.Run()                                                             // a failing step is part of the case; the output file is what counts
	b, err := os.ReadFile(filepath.Join(ws, ".pipemaster", "outputs", "ver")) // #nosec G304 -- temp dir
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func outputJob(last *ir.Step) *ir.Job {
	j := scriptJob("build", "echo first")
	j.Steps = append(j.Steps, last)
	j.Outputs = []ir.Output{{Name: "ver", Value: "1.2"}}
	return j
}

// GHA publishes the job outputs whatever the last step did.
func TestOutputsAreWrittenWhenTheLastStepIsSkipped(t *testing.T) {
	last := &ir.Step{ID: "last", Kind: ir.StepScript, Script: []string{"echo only-on-pr"}, If: eqStr([]string{"event"}, "pull_request")}
	files, _ := emit(t, project(outputJob(last)))
	if got := runLastContainer(t, files, "PM_CTX_EVENT=push"); got != "1.2" {
		t.Errorf("output ver = %q, want 1.2\n%s", got, joinFiles(files))
	}
}

func TestOutputsAreWrittenWhenTheLastStepFailsAndContinues(t *testing.T) {
	last := &ir.Step{ID: "last", Kind: ir.StepScript, Script: []string{"false", "echo unreachable"}, ContinueOnError: true}
	files, _ := emit(t, project(outputJob(last)))
	if got := runLastContainer(t, files); got != "1.2" {
		t.Errorf("output ver = %q, want 1.2\n%s", got, joinFiles(files))
	}
}

func TestOutputsAreWrittenOnARunThatPasses(t *testing.T) {
	last := &ir.Step{ID: "last", Kind: ir.StepScript, Script: []string{"echo ok"}}
	files, _ := emit(t, project(outputJob(last)))
	if got := runLastContainer(t, files); got != "1.2" {
		t.Errorf("output ver = %q, want 1.2", got)
	}
}

func TestOutputWritersRoundTrip(t *testing.T) {
	last := &ir.Step{ID: "last", Kind: ir.StepScript, Script: []string{"echo ok"}, If: eqStr([]string{"event"}, "pull_request"), ContinueOnError: true}
	p := project(outputJob(last))
	files, _ := emit(t, p)
	back := roundTrip(t, files)
	stripExt(back)
	ir.Normalize(p)
	if ok, diff := ir.Equal(p, back); !ok {
		t.Errorf("trip changed the job: %s\n%s", diff, joinFiles(files))
	}
}
