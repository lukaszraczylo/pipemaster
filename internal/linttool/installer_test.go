package linttool_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil { //nolint:gosec // test stub must be executable, path is a t.TempDir child
		t.Fatal(err)
	}
}

// A failing download must not leave its temporary directory behind.
func TestInstallerRemovesTempDirOnFailure(t *testing.T) {
	script, err := os.ReadFile("../../scripts/install-linters.sh")
	if err != nil {
		t.Fatal(err)
	}
	root, tmp, fake := t.TempDir(), t.TempDir(), t.TempDir()
	writeExec(t, filepath.Join(root, "scripts", "install-linters.sh"), string(script))
	// Stamped woodpecker-cli, so only the drone-cli path runs.
	bin := filepath.Join(root, ".tools", "bin")
	writeExec(t, filepath.Join(bin, "woodpecker-cli"), "#!/bin/sh\n")
	writeExec(t, filepath.Join(bin, ".woodpecker-cli-v3.20.1-3f1bb0bd0db28e410ad797034d75780116122c91"), "")
	writeExec(t, filepath.Join(fake, "curl"), "#!/bin/sh\nexit 22\n")

	cmd := exec.Command("bash", filepath.Join(root, "scripts", "install-linters.sh")) // #nosec G204 -- fixed test script
	cmd.Env = append(os.Environ(), "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+tmp)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the installer must fail when the download fails:\n%s", out)
	}
	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("the installer left %d temporary entries in %s", len(left), tmp)
	}
}
