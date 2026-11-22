package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dispatchWorkflow = `name: ci
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
  workflow_dispatch:
    inputs:
      level:
        type: choice
        options: [a, b]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`

const bareDispatchWorkflow = `name: ci
on:
  workflow_dispatch:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`

func writeTree(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil { // #nosec G306 G703 -- temp dir
		t.Fatal(err)
	}
}

func readTree(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel)) // #nosec G304 -- temp dir
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A GHA workflow with a manual trigger goes to Argo, gets one edit, and comes back with --force.
func TestEditedArgoFilesReturnToGHAWithTheManualTrigger(t *testing.T) {
	src, argoDir, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, src, ".github/workflows/ci.yml", dispatchWorkflow)
	if code, msg := runCLI("convert", src, "--to", "argo", "-o", argoDir); code != 0 {
		t.Fatalf("convert to argo = %d\n%s", code, msg)
	}
	file := ".argo/workflows/ci.yaml"
	edited := strings.Replace(readTree(t, argoDir, file), "echo hi", "echo bye", 1)
	if !strings.Contains(edited, "echo bye") {
		t.Fatalf("the edit did not apply:\n%s", edited)
	}
	writeTree(t, argoDir, file, edited)
	if code, msg := runCLI("convert", argoDir, "--from", "argo", "--to", "gha", "-o", back, "--force"); code != 0 {
		t.Fatalf("convert --force = %d\n%s", code, msg)
	}
	got := readTree(t, back, ".github/workflows/ci.yml")
	for _, want := range []string{"workflow_dispatch:", "push:", "pull_request:", "echo bye"} {
		if !strings.Contains(got, want) {
			t.Errorf("the GHA file lacks %q:\n%s", want, got)
		}
	}
}

// Without the metadata layer the manual trigger must still come back, from the marker in the template.
func TestManualTriggerSurvivesWithoutMetadata(t *testing.T) {
	src, argoDir, back := t.TempDir(), t.TempDir(), t.TempDir()
	writeTree(t, src, ".github/workflows/ci.yml", bareDispatchWorkflow)
	if code, msg := runCLI("convert", src, "--to", "argo", "-o", argoDir); code != 0 {
		t.Fatalf("convert to argo = %d\n%s", code, msg)
	}
	if err := os.RemoveAll(filepath.Join(argoDir, ".pipemaster")); err != nil {
		t.Fatal(err)
	}
	if code, msg := runCLI("convert", argoDir, "--from", "argo", "--to", "gha", "-o", back); code != 0 {
		t.Fatalf("convert = %d\n%s", code, msg)
	}
	if got := readTree(t, back, ".github/workflows/ci.yml"); !strings.Contains(got, "workflow_dispatch") {
		t.Errorf("the manual trigger was lost:\n%s", got)
	}
}
