package argo_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func triggerProject(trs ...ir.Trigger) *ir.Project {
	p := project(scriptJob("build", "echo hi"))
	p.Pipelines[0].Triggers = trs
	return p
}

func TestTriggersRoundTrip(t *testing.T) {
	cases := map[string][]ir.Trigger{
		"push branches":  {{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main", "release/*"}, Exclude: []string{"wip"}}}},
		"push no filter": {{Kind: ir.TriggerPush}},
		"tag":            {{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}, Exclude: []string{"v0"}}}},
		"tag all":        {{Kind: ir.TriggerTag}},
		"pull request":   {{Kind: ir.TriggerPR, Branches: ir.Filter{Include: []string{"main"}, Exclude: []string{"old"}}}},
		"schedule":       {{Kind: ir.TriggerSchedule, Cron: "0 1 * * *"}, {Kind: ir.TriggerSchedule, Cron: "5 4 * * 1"}},
		"mixed": {
			{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
			{Kind: ir.TriggerPR},
			{Kind: ir.TriggerSchedule, Cron: "0 0 * * *"},
		},
	}
	for name, trs := range cases {
		t.Run(name, func(t *testing.T) {
			p := triggerProject(trs...)
			files, _ := emit(t, p)
			back := roundTrip(t, files)
			stripExt(back)
			ir.Normalize(p)
			if ok, diff := ir.Equal(p, back); !ok {
				t.Fatalf("trip changed the triggers:\n%s\n%s", diff, joinFiles(files))
			}
		})
	}
}

func TestGlobExcludeIsNotEmitted(t *testing.T) {
	p := triggerProject(ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Exclude: []string{"wip/*"}}})
	files, rep := emit(t, p)
	if _, ok := files[".argo/events/sensor.yaml"]; ok {
		t.Fatalf("a trigger that would run on every branch was written:\n%s", joinFiles(files))
	}
	if len(entries(rep, "glob")) == 0 {
		t.Fatal("no report entry for the glob exclude")
	}
}

func TestPullRequestGatesForks(t *testing.T) {
	files, rep := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerPR}))
	sensor := files[".argo/events/sensor.yaml"]
	for _, want := range []string{"head == base", "body.pull_request.head.repo.full_name", "body.repository.full_name"} {
		if !strings.Contains(sensor, want) {
			t.Errorf("sensor lacks %q:\n%s", want, sensor)
		}
	}
	if len(entries(rep, "fork")) == 0 {
		t.Error("no report entry names the fork gate")
	}
}

func TestEventSourceNamesWebhookSecret(t *testing.T) {
	files, rep := emit(t, triggerProject(ir.Trigger{Kind: ir.TriggerPush}))
	src := files[".argo/events/eventsource.yaml"]
	for _, want := range []string{"pipemaster-github-webhook", "key: token"} {
		if !strings.Contains(src, want) {
			t.Errorf("eventsource lacks %q:\n%s", want, src)
		}
	}
	if len(entries(rep, "pipemaster-github-webhook")) == 0 {
		t.Error("the webhook secret is not reported as external")
	}
}
