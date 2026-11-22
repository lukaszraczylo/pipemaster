package roundtrip_test

import (
	"bytes"
	"errors"
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
	for _, name := range names(files) {
		scripts, err := scriptsOf(files[name])
		if err != nil {
			t.Errorf("%s: cannot read the emitted file: %v", name, err)
			continue
		}
		for _, s := range scripts {
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
