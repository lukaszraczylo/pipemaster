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

func TestRunnerSelectorBecomesAgentLabel(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"gpu"}, Selector: map[string]string{"region": "eu"}}
	body, rep := one(t, wpProject("drone", j))
	if !strings.Contains(body, "region: eu") || !strings.Contains(body, "gpu: \"true\"") {
		t.Errorf("a selector entry must become a labels key beside the tag:\n%s", body)
	}
	if !reportHas(rep, caps.JobRunner, caps.External) || !reportDetail(rep, caps.JobRunner, "region=eu") {
		t.Errorf("want an external entry that names the selector entry: %+v", rep.Entries)
	}
}

func TestRunnerSelectorThatIsNoLabelIsLost(t *testing.T) {
	for _, sel := range []map[string]string{{"bad key": "x"}, {"empty": ""}, {"platform": "x"}} {
		j := wpJob("ci", wpStep("s", "x"))
		j.Runner = ir.Runner{Selector: sel}
		body, rep := one(t, wpProject("drone", j))
		if !reportHas(rep, caps.JobRunner, caps.Lost) || strings.Contains(body, "bad key") {
			t.Errorf("%v: want a lost entry and no written key:\n%s\n%+v", sel, body, rep.Entries)
		}
	}
}

func TestMixedRunnerJobNamesLostKeyAndSetup(t *testing.T) {
	j := wpJob("ci", wpStep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"gpu", "has space"}}
	_, rep := one(t, wpProject("drone", j))
	if !reportHas(rep, caps.JobRunner, caps.Lost) || !reportDetail(rep, caps.JobRunner, "has space") || !reportDetail(rep, caps.JobRunner, "WOODPECKER_AGENT_LABELS=gpu=true") {
		t.Errorf("the entry must name the lost label and the agent setup: %+v", rep.Entries)
	}
}

func TestHostedLabelAndBadTagKeepBothTexts(t *testing.T) {
	for _, hosted := range []string{"macos-latest", "ubuntu-latest"} {
		j := wpJob("ci", wpStep("s", "x"))
		j.Runner = ir.Runner{Labels: []string{hosted, "gpu", "has space"}}
		_, rep := one(t, wpProject(ghaOrigin, j))
		for _, want := range []string{hosted, "has space", "WOODPECKER_AGENT_LABELS=gpu=true"} {
			if !reportDetail(rep, caps.JobRunner, want) {
				t.Errorf("%s: the job.runner entry must name %q: %+v", hosted, want, rep.Entries)
			}
		}
	}
}
