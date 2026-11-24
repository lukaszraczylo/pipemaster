package roundtrip_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const droneFilterDoc = "kind: pipeline\nname: p\nsteps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n  when:\n    branch: [main]\n    event: [pull_request]\n"

const dronePushDoc = "kind: pipeline\nname: p\nsteps:\n- name: s\n  image: alpine\n  commands: [echo hi]\n  when:\n    event: [push]\n"

func TestDroneFilterBecomesGHACondition(t *testing.T) {
	out, _ := convert(t, "drone", "gha", ".drone.yml", droneFilterDoc)
	if strings.Contains(out, "if: false") {
		t.Fatalf("the filter disabled the step:\n%s", out)
	}
	for _, want := range []string{"github.event_name == 'pull_request'", "github.ref_name == 'main'"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDronePushIsBranchPushOnGHA(t *testing.T) {
	out, _ := convert(t, "drone", "gha", ".drone.yml", dronePushDoc)
	if !strings.Contains(out, "github.event_name == 'push' && !github.event.deleted && github.ref_type == 'branch'") {
		t.Fatalf("push did not map to a branch push:\n%s", out)
	}
}

func TestDroneGoAwayCorpusKeepsConditions(t *testing.T) {
	src := corpusFile(t, "54-drone-go-away", ".drone.yml")
	out, _ := convert(t, "drone", "gha", ".drone.yml", src)
	if n := strings.Count(out, "if: false"); n >= strings.Count(out, "if:") {
		t.Fatalf("every condition is false (%d of %d)", n, strings.Count(out, "if:"))
	}
}

func corpusFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpusRoot, dir, name)) // #nosec G304 -- fixed corpus path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
