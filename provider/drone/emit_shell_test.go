package drone_test

import "testing"

func TestDefaultShellOfBashSourcesGetsACaveat(t *testing.T) {
	for _, origin := range []string{"gha", "gitlab"} {
		_, rep := emitText(t, gproject(origin, gjob("build", gstep("s", "make"))))
		if !hasEntry(rep, "bash-only syntax fails when the image sh is dash") {
			t.Errorf("%s: no caveat in %v", origin, rep.Entries)
		}
	}
	_, rep := emitText(t, gproject("tekton", gjob("build", gstep("s", "make"))))
	if hasEntry(rep, "bash-only syntax") {
		t.Errorf("a Tekton source must get no bash caveat: %v", rep.Entries)
	}
}
