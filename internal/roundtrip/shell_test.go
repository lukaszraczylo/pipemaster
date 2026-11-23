package roundtrip_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/provider"
	"go.yaml.in/yaml/v3"
)

// scriptKeys name the YAML keys whose strings or string lists are shell scripts in the CI dialects.
var scriptKeys = map[string]bool{"run": true, "commands": true, "script": true, "before_script": true, "after_script": true}

// exprRE matches a GHA expression, which is not shell syntax and is resolved before the shell runs.
var exprRE = regexp.MustCompile(`\$\{\{.*?\}\}`)

// shellArgsRE matches the args of an `sh -ec` step that GHA writes for a container step.
var shellArgsRE = regexp.MustCompile(`(?s)^-ec '(.*)'$`)

// argoTagRE matches an Argo tag, which Argo resolves or leaves as written before the shell runs.
var argoTagRE = regexp.MustCompile(`\{\{.*?\}\}`)

// posixShells are the run.path values of a Concourse task that runs its last arg as a POSIX shell script.
var posixShells = map[string]bool{"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true}

// bashMark prefixes the script of a task whose shell is bash, so that it is checked with bash -n.
const bashMark = "\x00bash\n"

// powershells are the run.path values of a Concourse task that PowerShell runs; sh -n cannot check them.
var powershells = map[string]bool{"powershell": true, "pwsh": true}

// concourseRun returns the script of a Concourse run mapping (path sh, args [-ec, script]) and whether the
// mapping is a PowerShell run, which has no sh -n form.
func concourseRun(m map[string]any) (script string, isShell, isPowershell bool) {
	path, _ := m["path"].(string)
	args, _ := m["args"].([]any)
	if len(args) < 2 {
		return "", false, false
	}
	if first, _ := args[0].(string); !strings.HasPrefix(first, "-") {
		return "", false, false
	}
	last, _ := args[len(args)-1].(string)
	base := path[strings.LastIndex(path, "/")+1:]
	return last, posixShells[base], powershells[base]
}

// powershellTasks counts the Concourse tasks of a file that run under PowerShell.
func powershellTasks(data []byte) (int, error) {
	var roots []any
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var root any
		err := dec.Decode(&root)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		roots = append(roots, root)
	}
	n := 0
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if _, _, ps := concourseRun(t); ps {
				n++
			}
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return n, nil
}

// shellCommand is true for a container command that runs its args as a script: `sh -ec`, `bash -ec`.
func shellCommand(v any) bool {
	cmd, ok := v.([]any)
	if !ok || len(cmd) != 2 {
		return false
	}
	shell, _ := cmd[0].(string)
	flag, _ := cmd[1].(string)
	return flag == "-ec" && strings.HasSuffix(shell, "sh")
}

// scriptsOf lists the shell scripts of a pipeline file.
func scriptsOf(data []byte) ([]string, error) {
	var roots []any
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var root any
		err := dec.Decode(&root)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	var out []string
	var walk func(v any)
	collect := func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case []any:
			var lines []string
			for _, e := range t {
				if s, ok := e.(string); ok {
					lines = append(lines, s)
				}
			}
			out = append(out, strings.Join(lines, "\n"))
		}
	}
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if script, isShell, _ := concourseRun(t); isShell {
				if path, _ := t["path"].(string); path == "bash" || strings.HasSuffix(path, "/bash") {
					script = bashMark + script
				}
				out = append(out, script)
			}
			if shellCommand(t["command"]) {
				if args, ok := t["args"].([]any); ok {
					for _, a := range args {
						collect(a)
					}
				}
			}
			for k, e := range t {
				switch {
				case scriptKeys[k]:
					collect(e)
				case k == "args":
					if s, ok := e.(string); ok {
						if m := shellArgsRE.FindStringSubmatch(s); m != nil {
							out = append(out, strings.ReplaceAll(m[1], `'\''`, "'"))
						}
					}
				}
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	for _, root := range roots {
		walk(root)
	}
	return out, nil
}

// checkShellSyntax fails on an emitted script that `sh -n` rejects.
func checkShellSyntax(t *testing.T, id string, files map[string][]byte) {
	t.Helper()
	for _, f := range shellFailures(t, id, files) {
		t.Error(f)
	}
}

// shellFailures lists the emitted scripts that sh -n (bash -n for a bash task) rejects.
func shellFailures(t *testing.T, id string, files map[string][]byte) []string {
	t.Helper()
	var out []string
	for _, name := range names(files) {
		scripts, err := scriptsOf(files[name])
		if err != nil {
			out = append(out, fmt.Sprintf("%s: cannot read the emitted file: %v", name, err))
			continue
		}
		for _, s := range scripts {
			shell := "sh"
			if rest, ok := strings.CutPrefix(s, bashMark); ok {
				shell, s = "bash", rest
			}
			cmd := exec.Command(shell, "-n") // #nosec G204 -- fixed binary
			s = exprRE.ReplaceAllString(s, "X")
			if id == provider.Argo {
				s = argoTagRE.ReplaceAllString(s, "X")
			}
			if id == provider.Woodpecker || id == provider.Drone {
				s = strings.ReplaceAll(s, "$$", "$") // both engines write a literal dollar as $$
			}
			cmd.Stdin = strings.NewReader(s)
			if b, err := cmd.CombinedOutput(); err != nil {
				out = append(out, fmt.Sprintf("%s: %s -n rejects the script: %s\n%s", name, shell, strings.TrimSpace(string(b)), s))
			}
		}
	}
	return out
}
