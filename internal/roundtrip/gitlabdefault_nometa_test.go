package roundtrip_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
)

const gitlabDefaultSrc = `default:
  image: alpine:3.20
plain:
  script:
    - echo plain
mr-only:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
  script:
    - echo mr
`

// TestGitLabDefaultWoodpeckerWithoutMeta checks that a Woodpecker file made from a GitLab default pipeline keeps its
// push and tag triggers when it is read with no metadata, and that no onward target adds a pull request run.
func TestGitLabDefaultWoodpeckerWithoutMeta(t *testing.T) {
	src := fstest.MapFS{".gitlab-ci.yml": {Data: []byte(gitlabDefaultSrc)}}
	proj, _, err := prov(t, "gitlab").Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	wp := prov(t, "woodpecker")
	files, _, err := wp.Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	mid, _, err := wp.Parse(toFS(files))
	if err != nil {
		t.Fatal(err)
	}
	if len(mid.Pipelines) != 1 {
		t.Fatalf("pipelines = %d", len(mid.Pipelines))
	}
	var kinds []ir.TriggerKind
	for _, tr := range mid.Pipelines[0].Triggers {
		kinds = append(kinds, tr.Kind)
	}
	if len(kinds) != 2 || kinds[0] != ir.TriggerPush || kinds[1] != ir.TriggerTag {
		t.Fatalf("triggers after a read with no metadata = %v, want [push tag]", kinds)
	}
	for _, to := range []string{"gitlab", "gha", "drone"} {
		out, _, err := prov(t, to).Emit(mid)
		if err != nil {
			t.Fatalf("emit %s: %v", to, err)
		}
		for name, b := range out {
			txt := string(b)
			if strings.Contains(txt, "pull_request") {
				t.Errorf("%s %s admits a pull request:\n%s", to, name, txt)
			}
			if to == "gha" && !strings.Contains(txt, "push:") {
				t.Errorf("gha %s lost the push trigger:\n%s", name, txt)
			}
			if to == "drone" && strings.Contains(txt, "name: plain") && !strings.Contains(txt, "- push") {
				t.Errorf("drone %s lost the push trigger:\n%s", name, txt)
			}
		}
	}
}

// TestDefaultDroneEvents checks that a pipeline with no workflow rules gets the manual and cron events on Drone,
// reports the sources with no Drone event as lost, and names no cron schedule.
func TestDefaultDroneEvents(t *testing.T) {
	src := fstest.MapFS{".gitlab-ci.yml": {Data: []byte(gitlabDefaultSrc)}}
	proj, _, err := prov(t, "gitlab").Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	files, rep, err := prov(t, "drone").Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(files[".drone.yml"])
	for _, want := range []string{"- push", "- tag", "- custom", "- cron"} {
		if !strings.Contains(txt, want) {
			t.Errorf("drone file lacks %q:\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "pull_request") {
		t.Errorf("drone file admits a pull request:\n%s", txt)
	}
	got := map[caps.Feature]caps.Level{}
	for _, e := range rep.Entries {
		got[e.Feature] = e.Level
	}
	for f, lvl := range map[caps.Feature]caps.Level{
		caps.TriggerSchedule: caps.External, caps.TriggerAPI: caps.Lost,
		caps.TriggerPipelineDone: caps.Lost, caps.TriggerResource: caps.Lost,
	} {
		if got[f] != lvl {
			t.Errorf("%s level = %v, want %v", f, got[f], lvl)
		}
	}
}
