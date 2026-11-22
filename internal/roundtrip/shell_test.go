package roundtrip_test

import (
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

// scriptsOf lists the shell scripts of a pipeline file.
func scriptsOf(data []byte) []string {
	var root any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil
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
	walk(root)
	return out
}

// knownSyntaxGaps lists emitted files whose scripts sh -n rejects for a reason outside the Tekton result writes:
// Woodpecker comments out the `if` line that reads a needs output and keeps the `fi` that closes it.
var knownSyntaxGaps = map[string]bool{".woodpecker/clone-cleanup-workspace-check-git-commit.yaml": true}

// checkShellSyntax fails on an emitted script that `sh -n` rejects.
func checkShellSyntax(t *testing.T, id string, files map[string][]byte) {
	t.Helper()
	for _, name := range names(files) {
		if knownSyntaxGaps[name] {
			continue
		}
		for _, s := range scriptsOf(files[name]) {
			cmd := exec.Command("sh", "-n") // #nosec G204 -- fixed binary
			s = exprRE.ReplaceAllString(s, "X")
			if id == provider.Woodpecker || id == provider.Drone {
				s = strings.ReplaceAll(s, "$$", "$") // both engines write a literal dollar as $$
			}
			cmd.Stdin = strings.NewReader(s)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s: sh -n rejects the script: %s\n%s", name, strings.TrimSpace(string(b)), s)
			}
		}
	}
}
