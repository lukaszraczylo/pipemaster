package roundtrip_test

import (
	"strings"
	"testing"
)

func repoDispatchWorkflow(on string) string {
	return "name: ci\non:\n" + on + "jobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n"
}

// A repository_dispatch that the GHA file itself names is the author's event, not an emulation.
func TestGHABareRepositoryDispatchConvertsWithoutLoss(t *testing.T) {
	for name, on := range map[string]string{
		"bare":  "  repository_dispatch:\n",
		"types": "  repository_dispatch:\n    types: [deploy]\n",
	} {
		t.Run(name, func(t *testing.T) {
			src, out := t.TempDir(), t.TempDir()
			writeTree(t, src, ".github/workflows/ci.yml", repoDispatchWorkflow(on))
			code, msg := runCLI("convert", src, "--to", "gha", "--strict", "-o", out)
			if code != 0 {
				t.Fatalf("convert --strict = %d\n%s", code, msg)
			}
			if strings.Contains(msg, "trigger.api") {
				t.Errorf("report has a trigger.api entry:\n%s", msg)
			}
			if code, msg := runCLI("roundtrip", src, "--via", "gha"); code != 0 {
				t.Errorf("roundtrip via gha = %d\n%s", code, msg)
			}
		})
	}
}

// An API trigger that names no event still gets the emulated entry in GHA.
func TestUnreadableConcourseTriggerReportsEmulatedAPI(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	writeTree(t, src, ".concourse/ci.yml", unreadableBranchPipeline)
	code, msg := runCLI("convert", src, "--from", "concourse", "--to", "gha", "-o", out)
	if code != 0 {
		t.Fatalf("convert = %d\n%s", code, msg)
	}
	if !strings.Contains(msg, "trigger.api") || !strings.Contains(msg, "names no event") {
		t.Errorf("the emulated trigger.api entry is missing:\n%s", msg)
	}
}
