package drone_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/drone"
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

const nodeDoc = `kind: pipeline
type: docker
name: default
node:
%s
steps:
  - name: s
    image: alpine:3
    commands:
      - x
`

func parseNode(t *testing.T, node string) ir.Runner {
	t.Helper()
	fsys := fstest.MapFS{".drone.yml": &fstest.MapFile{Data: []byte(strings.Replace(nodeDoc, "%s", node, 1))}}
	p, _, err := drone.New().Parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return p.Pipelines[0].Jobs[0].Runner
}

func TestNativeNodeSelectorStaysASelector(t *testing.T) {
	r := parseNode(t, "  region: eu")
	if len(r.Labels) != 0 || r.Selector["region"] != "eu" || len(r.Selector) != 1 {
		t.Errorf("node region: eu must stay a selector: %+v", r)
	}
}

func TestMixedNodeMapSplitsTagsAndSelector(t *testing.T) {
	r := parseNode(t, "  gpu: \"true\"\n  region: eu")
	if len(r.Labels) != 1 || r.Labels[0] != "gpu" || len(r.Selector) != 1 || r.Selector["region"] != "eu" {
		t.Fatalf("a true key is a tag and a value key is a selector: %+v", r)
	}
	text, _ := emitText(t, func() *ir.Project {
		j := gjob("ci", gstep("s", "x"))
		j.Runner = r
		return gproject("drone", j)
	}())
	if !strings.Contains(text, "node:\n  gpu: \"true\"\n  region: eu") {
		t.Errorf("the mixed map must come back with both kinds:\n%s", text)
	}
}

func TestMixedRunnerJobNamesLostKeyAndSetup(t *testing.T) {
	j := gjob("ci", gstep("s", "x"))
	j.Runner = ir.Runner{Labels: []string{"gpu", "has space"}}
	_, rep := emitText(t, gproject("woodpecker", j))
	for _, e := range rep.Entries {
		if e.Feature == caps.JobRunner {
			if e.Level != caps.Lost || !strings.Contains(e.Detail, "has space") || !strings.Contains(e.Detail, "DRONE_RUNNER_LABELS=gpu:true") {
				t.Errorf("the entry must name the lost label and the runner setup: %+v", e)
			}
			return
		}
	}
	t.Error("no job.runner entry")
}
