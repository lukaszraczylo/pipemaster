package drone_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
)

func TestResultWriteLineIsNotWritten(t *testing.T) {
	text, rep := emitText(t, gproject("tekton", gjob("ci", gstep("s", "echo v > $(results.tag.path)", "make"))))
	lost := false
	for _, e := range rep.Entries {
		lost = lost || e.Feature == caps.JobOutputs && e.Level == caps.Lost
	}
	if strings.Contains(text, "results.") || !lost {
		t.Fatalf("result write:\n%s\n%+v", text, rep.Entries)
	}
}
