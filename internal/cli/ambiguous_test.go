package cli

import (
	"strings"
	"testing"

	_ "github.com/lukaszraczylo/pipemaster/provider/gha"
)

const ciWorkflow = "on: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: echo hi\n"

func TestConvertAmbiguousNeedsFrom(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".github/workflows/ci.yml", ciWorkflow)
	write(t, dir, ".gitea/workflows/ci.yml", ciWorkflow)
	code, _, errs := run(t, "convert", dir, "--to", "gitlab", "-o", t.TempDir())
	if code != 2 {
		t.Fatalf("code=%d stderr=%q", code, errs)
	}
	for _, want := range []string{"gha", "gitea", "--from"} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr lacks %q: %q", want, errs)
		}
	}
}
