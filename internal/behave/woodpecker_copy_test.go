//go:build behave

package behave

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStripDependsOnKeepsTheRest(t *testing.T) {
	for _, tc := range []struct {
		name, in string
	}{
		{"list", "when:\n  - event: push\ndepends_on: [a]\nsteps:\n  - name: s\n    image: alpine\n    commands: [echo hi]\n"},
		{"none", "steps:\n  - name: s\n    image: alpine\n    commands: [echo hi]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stripDependsOn([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(got), "depends_on") || !strings.Contains(string(got), "echo hi") {
				t.Fatalf("stripped text = %q", got)
			}
		})
	}
	if _, err := stripDependsOn([]byte("- a\n")); err == nil {
		t.Fatal("a workflow that is no mapping must be an error")
	}
}

func TestWoodpeckerRunCopyStaysInTheRepoPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".woodpecker"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".woodpecker", "b.yaml"), []byte("depends_on: [a]\nsteps: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, err := woodpeckerRunCopy(dir, filepath.Join(".woodpecker", "b.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, rel)) // #nosec G304 -- a path under the test directory
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(rel) || strings.Contains(string(raw), "depends_on") {
		t.Fatalf("copy %s holds %q", rel, raw)
	}
}
