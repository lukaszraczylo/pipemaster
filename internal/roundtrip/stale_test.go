package roundtrip_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/cli"
	"github.com/lukaszraczylo/pipemaster/meta"
)

const exitStale = 4

func runCLI(args ...string) (int, string) {
	var out, errb bytes.Buffer
	code := cli.Run(args, &out, &errb)
	return code, errb.String()
}

func TestStaleRefuses(t *testing.T) {
	src := filepath.Join(featuresRoot, "gha", "features", "job.env")
	gl := t.TempDir()
	if code, msg := runCLI("convert", src, "--to", "gitlab", "-o", gl); code != 0 {
		t.Fatalf("convert to gitlab = %d\n%s", code, msg)
	}
	file := filepath.Join(gl, ".gitlab-ci.yml")
	data, err := os.ReadFile(file) // #nosec G304 -- temp dir path
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, "\n# edited by hand\n"...), 0o600); err != nil { // #nosec G703 -- path under t.TempDir
		t.Fatal(err)
	}

	m, err := meta.Load(os.DirFS(gl), nil)
	if err != nil || m == nil {
		t.Fatalf("load meta: %v %v", m, err)
	}
	stale, err := m.Stale(os.DirFS(gl))
	if err == nil || len(stale) != 1 || stale[0] != ".gitlab-ci.yml" {
		t.Fatalf("Stale = %v, %v; want [.gitlab-ci.yml] and an error", stale, err)
	}

	back := t.TempDir()
	code, msg := runCLI("convert", gl, "--to", "gha", "-o", back)
	if code != exitStale || !strings.Contains(msg, ".gitlab-ci.yml") {
		t.Fatalf("convert without --force = %d, want %d naming the file\n%s", code, exitStale, msg)
	}
	if code, msg := runCLI("convert", gl, "--to", "gha", "-o", back, "--force"); code != 0 {
		t.Fatalf("convert --force = %d\n%s", code, msg)
	}
}

const releaseNever = "  rules:\n    - when: never\n"

func TestForceKeepsUserEditOverStoredMetadata(t *testing.T) {
	src := t.TempDir()
	workflow := `name: CI
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: make
  release:
    needs: build
    if: ${{ always() && github.ref == 'refs/heads/main' }}
    runs-on: ubuntu-latest
    steps:
      - run: ./release.sh
  notify:
    needs: build
    if: ${{ always() && github.ref == 'refs/heads/dev' }}
    runs-on: ubuntu-latest
    steps:
      - run: ./notify.sh
  call:
    needs: build
    uses: org/repo/.github/workflows/r.yml@v1
    secrets: inherit
`
	if err := os.MkdirAll(filepath.Join(src, ".github", "workflows"), 0o750); err != nil { // #nosec G301 -- temp dir
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, ".github", "workflows", "ci.yml"), []byte(workflow), 0o600); err != nil { // #nosec G306 G703 -- temp dir
		t.Fatal(err)
	}
	gl := t.TempDir()
	if code, msg := runCLI("convert", src, "--to", "gitlab", "-o", gl); code != 0 {
		t.Fatalf("convert to gitlab = %d\n%s", code, msg)
	}
	file := filepath.Join(gl, ".gitlab-ci.yml")
	data, err := os.ReadFile(file) // #nosec G304 -- temp dir path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), releaseNever) {
		t.Fatalf("expected a never rule on release:\n%s", data)
	}
	head, tail, found := strings.Cut(string(data), "\nrelease:\n")
	if !found {
		t.Fatalf("no release job:\n%s", data)
	}
	edited := head + "\nrelease:\n" + strings.Replace(tail, releaseNever, "  rules:\n    - if: $CI_COMMIT_TAG\n", 1)
	if err := os.WriteFile(file, []byte(edited), 0o600); err != nil { // #nosec G703 -- temp dir
		t.Fatal(err)
	}
	back := t.TempDir()
	code, msg := runCLI("convert", gl, "--to", "gha", "-o", back, "--force")
	if code != 0 {
		t.Fatalf("convert --force = %d\n%s", code, msg)
	}
	out, err := os.ReadFile(filepath.Join(back, ".github", "workflows", "ci.yml")) // #nosec G304 -- temp dir path
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "refs/heads/main") {
		t.Errorf("stored metadata overwrote the edited condition:\n%s", out)
	}
	if !strings.Contains(string(out), "tag") {
		t.Errorf("the edited condition is missing:\n%s", out)
	}
	for _, want := range []string{"refs/heads/dev", "uses: org/repo/.github/workflows/r.yml@v1", "secrets: inherit"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("an unedited job lost %q although it still reads as emitted:\n%s", want, out)
		}
	}
	if !strings.Contains(msg, "edited after conversion") || !strings.Contains(msg, "job/release") {
		t.Errorf("report does not name the overridden entry:\n%s", msg)
	}
}

func TestForeignEmbeddedBlockInAnotherFileIsIgnored(t *testing.T) {
	src := t.TempDir()
	workflow := "on: push\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n"
	// The block lists a file this repository lacks, so applying it would report the run as stale.
	foreign := meta.Embed(map[string][]byte{"docs/example-converted.yml": []byte("a: 1\n"), "other/ci.yml": []byte("b: 1\n")},
		&meta.Meta{Version: meta.Version, Source: "gitlab", Dialect: "gitlab"})
	for name, body := range map[string][]byte{".github/workflows/ci.yml": []byte(workflow), "docs/example-converted.yml": foreign["docs/example-converted.yml"]} {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { // #nosec G301 -- temp dir
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil { // #nosec G703 -- temp dir
			t.Fatal(err)
		}
	}
	if code, msg := runCLI("convert", src, "--from", "gha", "--to", "gitlab", "-o", t.TempDir()); code != 0 {
		t.Fatalf("convert = %d; an unrelated embedded block must not decide the run\n%s", code, msg)
	}
}
