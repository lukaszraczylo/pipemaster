package woodpecker_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestDefaultShellOfBashSourcesGetsACaveat(t *testing.T) {
	for _, origin := range []string{"gha", "gitlab"} {
		_, rep := emitFiles(t, wpProject(origin, &ir.Job{ID: "build", Steps: []*ir.Step{wpStep("s", "make")}}))
		if !hasEntry(rep, "bash-only syntax fails when the image sh is dash") {
			t.Errorf("%s: no caveat in %v", origin, rep.Entries)
		}
	}
	_, rep := emitFiles(t, wpProject("tekton", &ir.Job{ID: "build", Steps: []*ir.Step{wpStep("s", "make")}}))
	if hasEntry(rep, "bash-only syntax") {
		t.Errorf("a Tekton source must get no bash caveat: %v", rep.Entries)
	}
}
