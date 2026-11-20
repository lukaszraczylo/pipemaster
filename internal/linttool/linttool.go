// Package linttool finds external linters for tests and runs them on emitted files.
package linttool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// RequireEnv makes a missing linter fail a test instead of skipping it.
const RequireEnv = "PIPEMASTER_REQUIRE_LINTERS"

const (
	toolsDir     = ".tools/bin"
	woodpeckerID = "woodpecker-cli"
)

// Find returns the path of a linter: first on PATH, then in .tools/bin of the enclosing module.
func Find(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			p := filepath.Join(dir, toolsDir, name)
			st, err := os.Stat(p)
			return p, err == nil && st.Mode().IsRegular()
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Require returns the path of the linter, or skips the test, or fails it when RequireEnv is 1.
func Require(t testing.TB, name string) string {
	t.Helper()
	p, ok := Find(name)
	if ok {
		return p
	}
	if os.Getenv(RequireEnv) == "1" {
		t.Fatalf("%s is not installed and %s=1; run scripts/install-linters.sh", name, RequireEnv)
	}
	t.Skipf("%s is not installed; run scripts/install-linters.sh", name)
	return ""
}

// RequireWoodpecker is Require for woodpecker-cli.
func RequireWoodpecker(t testing.TB) string { return Require(t, woodpeckerID) }

// WoodpeckerLint runs `woodpecker-cli lint --strict` on one workflow file and returns its output.
// The error is non-nil when the linter reports a problem.
func WoodpeckerLint(t testing.TB, name string, data []byte, extra ...string) (string, error) {
	t.Helper()
	bin := RequireWoodpecker(t)
	p := filepath.Join(t.TempDir(), filepath.Base(name))
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	args := append([]string{"--disable-update-check", "lint", "--strict"}, extra...)
	out, err := exec.Command(bin, append(args, p)...).CombinedOutput() // #nosec G204 -- pinned local binary, temp path
	return string(out), err
}
