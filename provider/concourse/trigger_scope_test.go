package concourse_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

const task = "  - task: t\n    config: {platform: linux, run: {path: ls}}\n"

// threeRoots has jobs that fire on a push, a schedule and a tag, each on its own resource.
const threeRoots = "resources:\n- {name: src, type: git, source: {uri: u, branch: main}}\n- {name: tick, type: time, source: {interval: 24h}}\n- {name: tags, type: git, source: {uri: u, tag_filter: 'v*'}}\n" +
	"jobs:\n- name: build\n  plan:\n  - {get: src, trigger: true}\n" + task +
	"- name: nightly-job\n  plan:\n  - {get: tick, trigger: true}\n" + task +
	"- name: rel\n  plan:\n  - {get: tags, trigger: true}\n" + task

func TestJobsKeepOnlyTheirOwnTriggers(t *testing.T) {
	pl, rep := parseYAML(t, threeRoots)
	if len(pl.Triggers) != 3 {
		t.Fatalf("pipeline triggers must stay the union: %+v", pl.Triggers)
	}
	for id, own := range map[string]string{"build": "src", "nightly-job": "tick", "rel": "tags"} {
		j := job(t, pl, id)
		if !j.Manual {
			t.Errorf("%s fires on one of three triggers and must be manual", id)
		}
		if got := ext(j.Ext)["triggers"]; !reflect.DeepEqual(got, []any{own}) {
			t.Errorf("%s Ext triggers = %v, want [%s]", id, got, own)
		}
		if !hasEntry(rep, "job "+id) {
			t.Errorf("no entry names job %s", id)
		}
	}
}

func TestNoJobRunsOnATriggerItLacksInGHA(t *testing.T) {
	pl, _ := parseYAML(t, threeRoots)
	out, _, err := gha.New(gha.GHA).Emit(&ir.Project{Pipelines: []*ir.Pipeline{pl}})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range out {
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if n := strings.Count(string(body), "workflow_dispatch'"); n < 3 {
			t.Errorf("%s: want all three jobs gated to manual starts, found %d guards:\n%s", name, n, body)
		}
	}
}

func TestSameRootSetStaysAutomatic(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n- {name: src, type: git, source: {uri: u, branch: main}}\njobs:\n- name: a\n  plan:\n  - {get: src, trigger: true}\n"+task+"- name: b\n  plan:\n  - {get: src, trigger: true}\n"+task)
	for _, j := range pl.Jobs {
		if j.Manual || ext(j.Ext)["triggers"] != nil {
			t.Errorf("%s must stay automatic with no triggers key: %+v", j.ID, j)
		}
	}
}

func TestJobWithAllTriggersStaysAutomatic(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n- {name: src, type: git, source: {uri: u, branch: main}}\n- {name: tick, type: time, source: {interval: 24h}}\njobs:\n- name: both\n  plan:\n  - {get: src, trigger: true}\n  - {get: tick, trigger: true}\n"+task+"- name: one\n  plan:\n  - {get: src, trigger: true}\n"+task)
	if job(t, pl, "both").Manual || !job(t, pl, "one").Manual {
		t.Fatalf("both=%v one=%v", job(t, pl, "both").Manual, job(t, pl, "one").Manual)
	}
}
