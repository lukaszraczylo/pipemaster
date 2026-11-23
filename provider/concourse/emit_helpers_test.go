package concourse_test

import (
	"os/exec"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/internal/linttool"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/concourse"
	"github.com/lukaszraczylo/pipemaster/report"
	"github.com/lukaszraczylo/pipemaster/validate"
)

// emit runs Emit on a valid project, checks every file against the embedded schema and fly --strict, and
// returns the files as text.
func emit(t *testing.T, p *ir.Project) (map[string]string, *report.Report) {
	t.Helper()
	ir.Normalize(p)
	raw, rep, err := concourse.New().Emit(p)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := map[string]string{}
	for n, b := range raw {
		out[n] = string(b)
		if err := validate.File("concourse", n, b); err != nil {
			t.Fatalf("schema rejects %s: %v\n%s", n, err, b)
		}
		if o, err := linttool.FlyLint(t, n, b); err != nil {
			t.Fatalf("fly rejects %s: %v\n%s\n%s", n, err, o, b)
		}
		checkScripts(t, string(b))
	}
	return out, rep
}

// checkScripts runs sh -n on every script of every task.
func checkScripts(t *testing.T, src string) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if run, ok := x["run"].(map[string]any); ok {
				if args, ok := run["args"].([]any); ok && len(args) > 0 {
					script, _ := args[len(args)-1].(string)
					if strings.Contains(script, "((") {
						t.Errorf("a ((var)) is in a script:\n%s", script)
					}
					if shell, _ := run["path"].(string); shell == "sh" || shell == "bash" {
						syntaxCheck(t, shell, script)
					}
				}
			}
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
}

// project makes a one-pipeline project from a foreign origin.
func project(jobs ...*ir.Job) *ir.Project {
	return &ir.Project{
		Origin:    ir.Origin{Provider: "gha", Dialect: "gha"},
		Pipelines: []*ir.Pipeline{{ID: "ci", Jobs: jobs}},
	}
}

func scriptJob(id string, script ...string) *ir.Job {
	return &ir.Job{ID: id, Steps: []*ir.Step{{ID: id + "-run", Kind: ir.StepScript, Script: script}}}
}

func pushOn(branches ...string) ir.Trigger {
	return ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: branches}}
}

// pipelineDoc decodes the only file of an emission.
func pipelineDoc(t *testing.T, files map[string]string) map[string]any {
	t.Helper()
	src, ok := files[".concourse/ci.yml"]
	if !ok {
		t.Fatalf("no .concourse/ci.yml in %v", keys(files))
	}
	var d map[string]any
	if err := yaml.Unmarshal([]byte(src), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func list(v any) []any { l, _ := v.([]any); return l }
func dict(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func jobDoc(t *testing.T, d map[string]any, name string) map[string]any {
	t.Helper()
	for _, j := range list(d["jobs"]) {
		if m := dict(j); m["name"] == name {
			return m
		}
	}
	t.Fatalf("job %s not in %v", name, d["jobs"])
	return nil
}

func resourceDoc(d map[string]any, name string) map[string]any {
	for _, r := range list(d["resources"]) {
		if m := dict(r); m["name"] == name {
			return m
		}
	}
	return nil
}

// reparse parses the emitted files back.
func reparse(t *testing.T, files map[string]string) (*ir.Project, *report.Report) {
	t.Helper()
	return parseFiles(t, stripDir(files))
}

func stripDir(files map[string]string) map[string]string {
	out := map[string]string{}
	for n, b := range files {
		out[strings.TrimPrefix(n, ".concourse/")] = b
	}
	return out
}

func hasFeatureEntry(rep *report.Report, feature, detail string) bool {
	for _, e := range rep.Entries {
		if string(e.Feature) == feature && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}

func callOf(name string) expr.Expr { return expr.Call{Name: name} }

func eqRef(ref, lit string) expr.Expr {
	return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: strings.Split(ref, ".")}, R: expr.Lit{Kind: expr.LitStr, Str: lit}}
}

// checkShellTasks runs the syntax check of each task's own shell on its script.
func checkShellTasks(t *testing.T, src string) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if run, ok := x["run"].(map[string]any); ok {
				shell, _ := run["path"].(string)
				args, _ := run["args"].([]any)
				if (shell == "sh" || shell == "bash") && len(args) > 0 {
					script, _ := args[len(args)-1].(string)
					syntaxCheck(t, shell, script)
				}
			}
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(doc)
}

// syntaxCheck runs sh -n or bash -n on the script.
func syntaxCheck(t *testing.T, shell, script string) {
	t.Helper()
	var cmd *exec.Cmd
	if shell == "bash" {
		cmd = exec.Command("bash", "-n")
	} else {
		cmd = exec.Command("sh", "-n")
	}
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("%s -n: %v %s\n%s", shell, err, out, script)
	}
}
