package drone_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestRunnerTagBecomesNodeLabel(t *testing.T) {
	j := gjob("ci", gstep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"gitlab-org"}, Selector: map[string]string{"zone": "a"}}
	text, rep := emitText(t, gproject("gitlab", j))
	if !strings.Contains(text, "node:\n  gitlab-org: \"true\"\n  zone: a") {
		t.Errorf("a tag must become a node key beside the selector:\n%s", text)
	}
	wantLevel(t, rep, caps.JobRunner, "", caps.External)
	for _, e := range rep.Entries {
		if e.Feature == caps.JobRunner && e.Level == caps.Lost {
			t.Errorf("a valid tag is not lost: %+v", e)
		}
		if e.Feature == caps.JobRunner && !strings.Contains(e.Detail, "DRONE_RUNNER_LABELS=gitlab-org:true") {
			t.Errorf("the entry must name the runner labels: %+v", e)
		}
	}
}

func TestRunnerTagWithNoValidKeyIsLost(t *testing.T) {
	j := gjob("ci", gstep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"has space"}}
	text, rep := emitText(t, gproject("gitlab", j))
	if strings.Contains(text, "has space") {
		t.Errorf("an invalid tag must not be written:\n%s", text)
	}
	wantLevel(t, rep, caps.JobRunner, "", caps.Lost)
}
