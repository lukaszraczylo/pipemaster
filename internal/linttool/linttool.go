// Package linttool finds external linters for tests and runs them on emitted files.
package linttool

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

// RequireEnv makes a missing linter fail a test instead of skipping it.
const RequireEnv = "PIPEMASTER_REQUIRE_LINTERS"

const (
	toolsDir      = ".tools/bin"
	woodpeckerID  = "woodpecker-cli"
	droneID       = "drone"
	kubeconformID = "kubeconform"
)

// pinned maps a linter to the version its --version output must end with; scripts/install-linters.sh builds it.
var pinned = map[string]string{woodpeckerID: "3.20.1", droneID: "1.9.0", kubeconformID: "0.8.0"}

// versionOK reports whether the binary at path is the pinned version. A linter without a pin always passes.
func versionOK(name, path string) bool {
	want, ok := pinned[name]
	if !ok {
		return true
	}
	flag := "--version"
	if name == kubeconformID {
		flag = "-v"
	}
	out, err := exec.Command(path, flag).Output() // #nosec G204 -- path of a local linter binary
	return err == nil && strings.HasSuffix(strings.TrimSpace(string(out)), want)
}

// Find returns the path of a linter at its pinned version: first on PATH, then in .tools/bin of the enclosing module.
func Find(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil && versionOK(name, p) {
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
			return p, err == nil && st.Mode().IsRegular() && versionOK(name, p)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Locate returns the path of the linter. When it is missing, Locate fails the test if RequireEnv is 1, and
// otherwise reports false so the caller can go on without it.
func Locate(t testing.TB, name string) (string, bool) {
	t.Helper()
	p, ok := Find(name)
	if !ok && os.Getenv(RequireEnv) == "1" {
		t.Fatalf("%s %s is not installed and %s=1; run scripts/install-linters.sh", name, pinned[name], RequireEnv)
	}
	return p, ok
}

// Require returns the path of the linter, or skips the test, or fails it when RequireEnv is 1.
func Require(t testing.TB, name string) string {
	t.Helper()
	p, ok := Locate(t, name)
	if !ok {
		t.Skipf("%s is not installed; run scripts/install-linters.sh", name)
	}
	return p
}

// privilegedPlugins are the plugins that real workflows run privileged; the linter rejects them otherwise. The
// Drone image plugin is the one that Drone files name.
var privilegedPlugins = []string{"woodpeckerci/plugin-docker-buildx", "plugins/docker"}

// lintArgs is the woodpecker-cli argument list that lints file with every privileged plugin allowed.
func lintArgs(extra []string, file string) []string {
	args := []string{"--disable-update-check", "lint", "--strict"}
	for _, plug := range privilegedPlugins {
		args = append(args, "--plugins-privileged", plug)
	}
	args = append(args, extra...)
	return append(args, file)
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
	out, err := exec.Command(bin, lintArgs(extra, p)...).CombinedOutput() // #nosec G204 -- pinned local binary, temp path
	return string(out), err
}

// RequireDrone is Require for drone.
func RequireDrone(t testing.TB) string { return Require(t, droneID) }

// DroneLint runs `drone lint --trusted` on one file and returns its output. The error is non-nil when the
// linter reports a problem. The linter checks only documents with an explicit type (research drone.md 9).
func DroneLint(t testing.TB, name string, data []byte) (string, error) {
	t.Helper()
	bin := RequireDrone(t)
	p := filepath.Join(t.TempDir(), filepath.Base(name))
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "lint", "--trusted", p).CombinedOutput() // #nosec G204 -- pinned local binary, temp path
	return string(out), err
}

// RequireKubeconform is Require for kubeconform.
func RequireKubeconform(t testing.TB) string { return Require(t, kubeconformID) }

// kubeconformArgs is the kubeconform argument list that checks file against the schemas in dir. The two locations
// cover the Tekton file names (kind_version) and the batch/v1 CronJob name (kind-group-version). There is no
// default location, so a test never uses the network.
func kubeconformArgs(dir, file string) []string {
	return []string{
		"-strict", "-summary",
		"-schema-location", filepath.Join(dir, "{{.ResourceKind}}_{{.ResourceAPIVersion}}.json"),
		"-schema-location", filepath.Join(dir, "{{.ResourceKind}}-{{.Group}}-{{.ResourceAPIVersion}}.json"),
		file,
	}
}

// KubeconformLint runs `kubeconform -strict` on one file against the embedded Tekton and CronJob schemas and
// returns its output. The error is non-nil when a document is invalid or has no schema.
func KubeconformLint(t testing.TB, name string, data []byte) (string, error) {
	t.Helper()
	bin := RequireKubeconform(t)
	tmp := t.TempDir()
	schemaDir := filepath.Join(tmp, "schemas")
	if err := os.Mkdir(schemaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(schemas.Tekton, "tekton")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(schemas.Tekton, "tekton/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(schemaDir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(tmp, filepath.Base(name))
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, kubeconformArgs(schemaDir, p)...).CombinedOutput() // #nosec G204 -- pinned local binary, temp paths
	return string(out), err
}
