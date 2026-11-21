package cli

import (
	"strings"
	"testing"

	_ "github.com/lukaszraczylo/pipemaster/provider/drone"
	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
	_ "github.com/lukaszraczylo/pipemaster/provider/gitlab"
	_ "github.com/lukaszraczylo/pipemaster/provider/woodpecker"
)

const ciWorkflow = "on: push\njobs:\n  %s:\n    runs-on: x\n    steps:\n      - run: echo hi\n"

// bothDirs holds two distinguishable workflows, so a run shows which directory it read.
func bothDirs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, ".github/workflows/ghaflow.yml", strings.Replace(ciWorkflow, "%s", "ghajob", 1))
	write(t, dir, ".gitea/workflows/giteaflow.yml", strings.Replace(ciWorkflow, "%s", "giteajob", 1))
	return dir
}

func TestDetectBothDirsListsBoth(t *testing.T) {
	code, out, errs := run(t, "detect", bothDirs(t))
	if code != 0 || out != "gha\ngitea\n" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, errs)
	}
}

func TestConvertAmbiguousNeedsFrom(t *testing.T) {
	code, _, errs := run(t, "convert", bothDirs(t), "--to", "gitlab", "-o", t.TempDir())
	if code != 2 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if !strings.Contains(errs, "several providers match") || !strings.Contains(errs, "gha, gitea") || !strings.Contains(errs, "--from") {
		t.Errorf("stderr does not name both providers and --from: %q", errs)
	}
}

func TestConvertFromGiteaReadsOnlyGiteaDir(t *testing.T) {
	out := t.TempDir()
	code, _, errs := run(t, "convert", bothDirs(t), "--from", "gitea", "--to", "gitlab", "-o", out)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	got := read(t, out, ".gitlab-ci.yml")
	if !strings.Contains(got, "giteajob") || strings.Contains(got, "ghajob") {
		t.Fatalf("output is not from .gitea only:\n%s", got)
	}
}

func TestConvertToDroneKeepsWorkingDirectory(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, src, ".github/workflows/ci.yml", "name: ci\non: push\njobs:\n  b:\n    runs-on: ubuntu-latest\n    steps:\n      - run: ls\n        working-directory: sub\n")
	code, _, errs := run(t, "convert", src, "--from", "gha", "--to", "drone", "-o", out)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	if got := read(t, out, ".drone.yml"); !strings.Contains(got, "working_dir: sub") {
		t.Fatalf("working_dir missing:\n%s", got)
	}
}
