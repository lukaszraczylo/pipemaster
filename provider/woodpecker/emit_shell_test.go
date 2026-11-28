package woodpecker_test

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestDefaultShellOfBashSourcesGetsTheWrapper(t *testing.T) {
	for _, origin := range []string{"gha", "gitlab"} {
		_, rep := emitFiles(t, wpProject(origin, &ir.Job{ID: "build", Steps: []*ir.Step{wpStep("s", "make")}}))
		if !hasEntry(rep, "bash-if-present wrapper command") {
			t.Errorf("%s: no wrapper entry in %v", origin, rep.Entries)
		}
	}
	_, rep := emitFiles(t, wpProject("tekton", &ir.Job{ID: "build", Steps: []*ir.Step{wpStep("s", "make")}}))
	if hasEntry(rep, "bash-if-present wrapper command") {
		t.Errorf("a Tekton source must get no bash wrapper: %v", rep.Entries)
	}
}
