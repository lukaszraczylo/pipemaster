package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestRunnerTagBecomesAgentLabel(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"gitlab-org", "linux/arm64"}}
	body, rep := one(t, wpProject("gitlab", j))
	if !strings.Contains(body, "labels:\n  platform: linux/arm64\n  gitlab-org: \"true\"") {
		t.Errorf("a tag must become a labels key beside platform:\n%s", body)
	}
	if !reportHas(rep, caps.JobRunner, caps.External) || !reportDetail(rep, caps.JobRunner, "WOODPECKER_AGENT_LABELS=gitlab-org=true") {
		t.Errorf("want an external entry that names the agent labels: %+v", rep.Entries)
	}
	if reportHas(rep, caps.JobRunner, caps.Lost) {
		t.Errorf("a valid tag is not lost: %+v", rep.Entries)
	}
}

func TestRunnerTagWithNoValidKeyIsLost(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"has space", "platform"}}
	body, rep := one(t, wpProject("gitlab", j))
	if strings.Contains(body, "has space") || !reportHas(rep, caps.JobRunner, caps.Lost) {
		t.Errorf("a tag with no valid key is lost, never written:\n%s", body)
	}
}
