package linttool_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/linttool"
)

func fakeCLI(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'woodpecker-cli version " + version + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "woodpecker-cli"), []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestFindAcceptsThePinnedVersion(t *testing.T) {
	dir := fakeCLI(t, "3.20.1")
	p, ok := linttool.Find("woodpecker-cli")
	if !ok || p != filepath.Join(dir, "woodpecker-cli") {
		t.Fatalf("Find = %q, %v; want the PATH binary", p, ok)
	}
}

func TestFindRejectsAnotherVersionOnPath(t *testing.T) {
	dir := fakeCLI(t, "3.19.0")
	if p, _ := linttool.Find("woodpecker-cli"); p == filepath.Join(dir, "woodpecker-cli") {
		t.Fatalf("Find returned the PATH binary of another version: %s", p)
	}
}

func fakeDrone(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'drone version " + version + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "drone"), []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestFindAcceptsThePinnedDroneVersion(t *testing.T) {
	dir := fakeDrone(t, "1.9.0")
	p, ok := linttool.Find("drone")
	if !ok || p != filepath.Join(dir, "drone") {
		t.Fatalf("Find = %q, %v; want the PATH binary", p, ok)
	}
}

func TestFindRejectsAnotherDroneVersionOnPath(t *testing.T) {
	dir := fakeDrone(t, "1.8.0")
	if p, _ := linttool.Find("drone"); p == filepath.Join(dir, "drone") {
		t.Fatalf("Find returned the PATH binary of another version: %s", p)
	}
}
