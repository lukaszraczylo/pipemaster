package emitkit

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestFailLoudRunsAndExits1(t *testing.T) {
	line := FailLoud(caps.StepAction, "pipeline/p/job/j/step/s", "no form `x` $(id) 'q' ${{ a }} ((b))\nnext")
	if !IsFailLoud([]string{line}) {
		t.Fatalf("not recognised: %s", line)
	}
	cmd := exec.Command("sh", "-c", line) // #nosec G204 -- fixed binary, generated script
	var se strings.Builder
	cmd.Stderr = &se
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	got := se.String()
	if !strings.HasPrefix(got, "pipemaster: lost: step.action pipeline/p/job/j/step/s: no form") || strings.Contains(got, "uid=") {
		t.Fatalf("stderr = %q", got)
	}
	if strings.ContainsAny(line, "${}`") || strings.Contains(line, "((") {
		t.Fatalf("template characters survive: %s", line)
	}
}

func TestIsFailLoudRejectsOthers(t *testing.T) {
	for _, s := range []string{"true", "exit 1", failLoudHead + "a' ; rm x; echo '" + failLoudTail, ""} {
		if IsFailLoudLine(s) {
			t.Errorf("accepted %q", s)
		}
	}
}
