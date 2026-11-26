package roundtrip_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const sparseEnvKey = "PIPEMASTER_CHECKOUT_SPARSE"

// envValue finds the value of key in any mapping of the emitted files.
func envValue(t *testing.T, files map[string][]byte, key string) (string, bool) {
	t.Helper()
	var found string
	var ok bool
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == key {
					found, ok = n.Content[i+1].Value, true
				}
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
	return found, ok
}

// A multi-line sparse-checkout list reaches git as one argument for each line, taken from the value that the
// target file carries.
func TestSparseListReachesGitAsSeparateArguments(t *testing.T) {
	list := "src\nmy docs\n*.md"
	for _, to := range []string{"gitlab", "woodpecker", "drone"} {
		t.Run(to, func(t *testing.T) {
			files, _, err := prov(t, to).Emit(oneJob(checkoutWith(map[string]ir.Value{"sparse-checkout": litv(list)})))
			if err != nil {
				t.Fatal(err)
			}
			var script string
			for _, s := range scriptCandidates(t, files) {
				if strings.Contains(s, "PIPEMASTER_CHECKOUT_PATH") && strings.Contains(s, "git ") {
					script = s
				}
			}
			if script == "" {
				t.Fatalf("no git script in %v", names(files))
			}
			// GitLab writes the value as an export line of the script; the other targets write an env entry.
			value, ok := envValue(t, files, sparseEnvKey)
			if !ok && to != "gitlab" {
				t.Fatalf("no %s in the emitted files", sparseEnvKey)
			}
			bin, logFile := t.TempDir(), filepath.Join(t.TempDir(), "argv")
			shim := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"[$a]\" >> \"$GIT_ARGV_LOG\"; done\nprintf -- '--\\n' >> \"$GIT_ARGV_LOG\"\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700); err != nil { //nolint:gosec // test stub must be executable
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "step.sh")
			if err := os.WriteFile(file, []byte(strings.ReplaceAll(script, "$$", "$")), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", file) //nolint:gosec // fixed binary, generated script
			cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "GIT_ARGV_LOG=" + logFile}
			if ok {
				cmd.Env = append(cmd.Env, sparseEnvKey+"="+strings.ReplaceAll(value, "$$", "$"))
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("script: %v\n%s", err, out)
			}
			got, err := os.ReadFile(logFile) //nolint:gosec // test temp path
			if err != nil {
				t.Fatal(err)
			}
			want := "[-C]\n[.]\n[sparse-checkout]\n[set]\n[--]\n[src]\n[my docs]\n[*.md]\n--\n"
			if string(got) != want {
				t.Errorf("git argv:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
