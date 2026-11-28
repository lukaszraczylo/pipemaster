package drone_test

import "testing"

func TestDefaultShellOfBashSourcesGetsTheWrapper(t *testing.T) {
	for _, origin := range []string{"gha", "gitlab"} {
		_, rep := emitText(t, gproject(origin, gjob("build", gstep("s", "make"))))
		if !hasEntry(rep, "bash-if-present wrapper command") {
			t.Errorf("%s: no wrapper entry in %v", origin, rep.Entries)
		}
	}
	_, rep := emitText(t, gproject("tekton", gjob("build", gstep("s", "make"))))
	if hasEntry(rep, "bash-if-present wrapper command") {
		t.Errorf("a Tekton source must get no bash wrapper: %v", rep.Entries)
	}
}
