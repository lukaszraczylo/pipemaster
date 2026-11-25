package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hostedWorkflow = `name: ci
on: push
jobs:
  build:
    runs-on: %s
    steps:
      - run: echo hi
`

func convertText(t *testing.T, from, to, name, content string) (string, string) {
	t.Helper()
	in, out := t.TempDir(), t.TempDir()
	path := filepath.Join(in, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, msg := runOut("convert", in, "--from", from, "--to", to, "-o", out)
	if code != 0 {
		t.Fatalf("convert %s->%s = %d\n%s", from, to, code, msg)
	}
	var all strings.Builder
	for _, b := range outputFiles(t, out) {
		all.Write(b)
	}
	return all.String(), msg
}

func TestHostedRunnerLabelNamesNoTagOrLabelOnOtherEngines(t *testing.T) {
	keys := map[string]string{"gitlab": "tags:", "concourse": "tags:", "woodpecker": "labels:", "tekton": "nodeSelector", "argo": "nodeSelector"}
	for to, key := range keys {
		for _, label := range []string{"ubuntu-latest", "ubuntu-22.04", "macos-14", "windows-2022"} {
			t.Run(to+"/"+label, func(t *testing.T) {
				out, msg := convertText(t, "gha", to, ".github/workflows/ci.yml", strings.Replace(hostedWorkflow, "%s", label, 1))
				for _, bad := range []string{key} {
					if strings.Contains(out, bad) {
						t.Fatalf("output has %q:\n%s", bad, out)
					}
				}
				if !strings.Contains(msg, "job.runner") {
					t.Fatalf("report names no job.runner verdict:\n%s", msg)
				}
			})
		}
	}
}

func TestSelfHostedLabelStaysATag(t *testing.T) {
	src := strings.Replace(hostedWorkflow, "%s", "[self-hosted, gpu]", 1)
	out, _ := convertText(t, "gha", "gitlab", ".github/workflows/ci.yml", src)
	if !strings.Contains(out, "tags:") || !strings.Contains(out, "gpu") {
		t.Fatalf("self-hosted labels must stay tags:\n%s", out)
	}
}

func TestGitLabTagsBecomeSelfHostedRunsOn(t *testing.T) {
	src := "build:\n  tags: [docker, linux]\n  script: [echo hi]\n"
	out, msg := convertText(t, "gitlab", "gha", ".gitlab-ci.yml", src)
	if !strings.Contains(out, "self-hosted") || strings.Contains(out, "ubuntu-latest") {
		t.Fatalf("tags must map to runs-on with self-hosted, never a hosted image:\n%s", out)
	}
	if !strings.Contains(msg, "[external]") || !strings.Contains(msg, "job.runner") {
		t.Fatalf("no external runner verdict:\n%s", msg)
	}
}

const bashContainerJob = `build:
  image: bash:5.2
  script:
    - arr=(a b)
    - echo "${arr[0]}"
`

func TestGitLabBashScriptInContainerRunsUnderBashOnGHA(t *testing.T) {
	out, msg := convertText(t, "gitlab", "gha", ".gitlab-ci.yml", bashContainerJob)
	for _, want := range []string{"command -v bash", "exec bash -e", "arr=(a b)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("workflow lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "shell: bash") {
		t.Fatalf("a pinned bash fails on an image with no bash:\n%s", out)
	}
	if !strings.Contains(msg, "step.shell") {
		t.Fatalf("no step.shell verdict:\n%s", msg)
	}
}

func TestBashWrapperOnGHAComesBackAsTheScript(t *testing.T) {
	in, mid, back := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(in, ".gitlab-ci.yml"), []byte(bashContainerJob), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, hop := range [][3]string{{in, "gha", mid}, {mid, "gitlab", back}} {
		from := "gitlab"
		if hop[1] == "gitlab" {
			from = "gha"
		}
		if code, _, msg := runOut("convert", hop[0], "--from", from, "--to", hop[1], "-o", hop[2]); code != 0 {
			t.Fatalf("convert to %s = %d\n%s", hop[1], code, msg)
		}
	}
	got := string(outputFiles(t, back)[".gitlab-ci.yml"])
	if strings.Contains(got, "command -v bash") || !strings.Contains(got, "arr=(a b)") {
		t.Fatalf("the wrapper did not collapse:\n%s", got)
	}
}
