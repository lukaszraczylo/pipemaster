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

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
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
		if m, ok := root.(map[string]any); ok {
			if jobs, ok := m["jobs"].([]any); ok {
				m["jobs"] = withoutTagTwins(jobs)
			}
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

// concourseTask is a task of a job plan that runs a shell script.
type concourseTask struct {
	job, name, shell, script string
}

// concourseTasks lists the shell tasks of the jobs of a Concourse file, each with its job and task name.
func concourseTasks(data []byte) ([]concourseTask, error) {
	var out []concourseTask
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var root map[string]any
		err := dec.Decode(&root)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		jobs, _ := root["jobs"].([]any)
		for _, j := range withoutTagTwins(jobs) {
			jm, _ := j.(map[string]any)
			job, _ := jm["name"].(string)
			var walk func(v any)
			walk = func(v any) {
				switch x := v.(type) {
				case []any:
					for _, e := range x {
						walk(e)
					}
				case map[string]any:
					if name, ok := x["task"].(string); ok {
						cfg, _ := x["config"].(map[string]any)
						run, _ := cfg["run"].(map[string]any)
						if script, isShell, _ := concourseRun(run); isShell {
							shell := "sh"
							if p, _ := run["path"].(string); p == "bash" || strings.HasSuffix(p, "/bash") {
								shell = "bash"
							}
							out = append(out, concourseTask{job: job, name: name, shell: shell, script: script})
						}
					}
					for _, e := range x {
						walk(e)
					}
				}
			}
			walk(jm["plan"])
		}
	}
}

// syntaxError runs shell -n on the script and returns its complaint, or an empty string.
func syntaxError(shell, script string) string {
	cmd := exec.Command(shell, "-n") // #nosec G204 -- fixed binary
	cmd.Stdin = strings.NewReader(exprRE.ReplaceAllString(script, "X"))
	if b, err := cmd.CombinedOutput(); err != nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func TestPowershellReportCoversOnlyItsOwnTask(t *testing.T) {
	rep := &report.Report{Entries: []report.Entry{{
		IRPath: "pipeline/ci/job/Win 21H2/step/script", Feature: caps.StepShell, Level: caps.Lost,
		Detail: "the script may be PowerShell, but the IR names no shell",
	}}}
	if !reportedPowershell(rep, concourseTask{job: "win-21h2", name: "script"}) {
		t.Fatal("the reported task is not covered")
	}
	for _, other := range []concourseTask{{job: "win-1809", name: "script"}, {job: "win-21h2", name: "other"}} {
		if reportedPowershell(rep, other) {
			t.Fatalf("%+v is covered by the entry of another task", other)
		}
	}
}

// withoutTagTwins drops the <job>-tag copy of a Concourse job: it repeats the tasks of its job, which the report names once.
func withoutTagTwins(jobs []any) []any {
	have := map[string]bool{}
	for _, j := range jobs {
		jm, _ := j.(map[string]any)
		name, _ := jm["name"].(string)
		have[name] = true
	}
	var out []any
	for _, j := range jobs {
		jm, _ := j.(map[string]any)
		name, _ := jm["name"].(string)
		if base, twin := strings.CutSuffix(name, "-tag"); twin && have[base] {
			continue
		}
		out = append(out, j)
	}
	return out
}
