package argo_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
)

// scriptOf is the script in the args of a container: the only arg, or the last of the bash-if-present wrapper.
func scriptOf(args []any) (string, bool) {
	switch {
	case len(args) == 1:
		s, ok := args[0].(string)
		return s, ok
	case len(args) == 3 && args[0] == provider.BashOrShScript:
		s, ok := args[2].(string)
		return s, ok
	}
	return "", false
}

// scripts lists every container script of the workflow files.
func scripts(t *testing.T, files map[string]string) []string {
	t.Helper()
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case doc:
			if args, ok := x["args"].([]any); ok {
				if cmd, _ := x["command"].([]any); len(cmd) == 2 {
					if script, ok := scriptOf(args); ok {
						out = append(out, script)
					}
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	for name, src := range files {
		if strings.HasPrefix(name, ".argo/workflows/") {
			for _, d := range docs(t, src) {
				walk(d)
			}
		}
	}
	return out
}

func shellOK(t *testing.T, script string) {
	t.Helper()
	// dash is /bin/sh on Debian and Ubuntu images, and it rejects bash-only syntax that the sh of macOS accepts.
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell = "sh"
	}
	cmd := exec.Command(shell, "-n") // #nosec G204 -- fixed binary
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("sh -n rejects the script: %v\n%s\n%s", err, out, script)
	}
}

func TestEveryScriptPassesShellSyntaxCheck(t *testing.T) {
	j := scriptJob("build",
		"echo ${{ sha }} ${{ env.A }} ${{ secrets.K }} ${{ vars.V }}",
		"echo \"$(results.out.path)\"",
		"echo ${{ inputs.x }} {{workflow.parameters.x}} '{{foo}}'",
		"if [ \"${{ branch }}\" = main ]; then echo yes; fi",
	)
	j.Outputs = []ir.Output{{Name: "o", Value: "lit'eral \"q\""}}
	j.Steps[0].If = eqStr([]string{"branch"}, "main")
	j.Steps[0].ContinueOnError = true
	p := project(j)
	p.Pipelines[0].Inputs = []ir.Input{{Name: "x", Default: "1"}}
	files, _ := emit(t, p)
	list := scripts(t, files)
	if len(list) == 0 {
		t.Fatal("no script found")
	}
	for _, s := range list {
		shellOK(t, s)
	}
	shellOK(t, provider.BashOrShScript)
}

func TestContextIsReadFromEnvNeverSubstituted(t *testing.T) {
	files, _ := emit(t, project(scriptJob("build", "echo ${{ branch }} ${{ sha }}")))
	for _, s := range scripts(t, files) {
		if strings.Contains(s, "ctx-") || strings.Contains(s, "workflow.parameters.ctx") {
			t.Errorf("a context tag is in the script text:\n%s", s)
		}
		if !strings.Contains(s, "${PM_CTX_BRANCH}") || !strings.Contains(s, "${PM_CTX_SHA}") {
			t.Errorf("the script does not read the env variables:\n%s", s)
		}
	}
	if !strings.Contains(joinFiles(files), "'{{workflow.parameters.ctx-branch}}'") {
		t.Errorf("the env value is not the context parameter:\n%s", joinFiles(files))
	}
}

func TestUserInputTagStaysInScript(t *testing.T) {
	p := project(scriptJob("build", "echo ${{ inputs.ver }}"))
	p.Pipelines[0].Inputs = []ir.Input{{Name: "ver", Default: "1"}}
	files, _ := emit(t, p)
	if s := scripts(t, files); len(s) != 1 || !strings.Contains(s[0], "{{workflow.parameters.ver}}") {
		t.Errorf("want {{workflow.parameters.ver}} in the script, got %q", s)
	}
}

func TestLiteralTagHandling(t *testing.T) {
	cases := map[string]struct {
		line   string
		report string
		kept   bool
		lost   bool
	}{
		"custom root passes":    {line: "echo {{foo.bar}}", kept: true, report: "simple_template.go:51-55"},
		"expression is lost":    {line: "echo {{=1+1}}", lost: true},
		"reserved root is lost": {line: "echo {{workflow.name}}", lost: true},
		"unterminated is lost":  {line: "echo {{ oops", lost: true},
		"item root is lost":     {line: "echo {{item}}", lost: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			files, rep := emit(t, project(scriptJob("build", c.line, "echo after")))
			all := joinFiles(files)
			s := scripts(t, files)
			if len(s) != 1 {
				t.Fatalf("scripts = %v", s)
			}
			shellOK(t, s[0])
			if got := strings.Contains(s[0], c.line); got != c.kept {
				t.Errorf("line kept = %v, want %v\n%s", got, c.kept, s[0])
			}
			if !strings.Contains(s[0], "echo after") {
				t.Errorf("the later statement is gone:\n%s", s[0])
			}
			if c.lost && !strings.Contains(all, "pipemaster:lost") {
				t.Errorf("no lost comment:\n%s", all)
			}
			if c.lost && len(entries(rep, "Argo substitutes")) == 0 {
				t.Errorf("no lost report entry")
			}
			if c.report != "" && len(entries(rep, c.report)) == 0 {
				t.Errorf("no report entry citing %s", c.report)
			}
		})
	}
}

func TestResultPathStaysValidShellAndIsReported(t *testing.T) {
	files, rep := emit(t, project(scriptJob("build", "echo v > $(results.out.path)")))
	for _, s := range scripts(t, files) {
		shellOK(t, s)
	}
	if len(entries(rep, "result file")) == 0 {
		t.Error("the $(results.) text is not reported")
	}
}
