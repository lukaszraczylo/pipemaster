package concourse_test

import (
	"reflect"
	"strings"
	"testing"

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
		if j.Manual == (id == "build") {
			t.Errorf("%s fires on one of three triggers: build takes a push condition, the others are manual (manual=%v)", id, j.Manual)
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
	proj, _ := parseFiles(t, map[string]string{"ci.yml": threeRoots})
	out, _, err := gha.New(gha.GHA).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range out {
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		if n := strings.Count(string(body), "workflow_dispatch'"); n < 2 {
			t.Errorf("%s: want the two jobs with no push gated to manual starts, found %d guards:\n%s", name, n, body)
		}
		if !strings.Contains(string(body), "github.ref_name == 'main'") {
			t.Errorf("%s: build must run only on a push to main:\n%s", name, body)
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
	both, one := job(t, pl, "both"), job(t, pl, "one")
	if both.Manual || both.If != nil || one.Manual || one.If == nil {
		t.Fatalf("both must stay plain and one must take a push condition: both=%v/%q one=%v/%q", both.Manual, cond(both.If), one.Manual, cond(one.If))
	}
}
