package gitlab_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func kinds(pl *ir.Pipeline) []ir.TriggerKind {
	var out []ir.TriggerKind
	for _, tr := range pl.Triggers {
		out = append(out, tr.Kind)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedKinds(k ...ir.TriggerKind) []ir.TriggerKind {
	sort.Slice(k, func(i, j int) bool { return k[i] < k[j] })
	return k
}

func trigger(t *testing.T, pl *ir.Pipeline, k ir.TriggerKind) ir.Trigger {
	t.Helper()
	for _, tr := range pl.Triggers {
		if tr.Kind == k {
			return tr
		}
	}
	t.Fatalf("no %s trigger in %v", k, kinds(pl))
	return ir.Trigger{}
}

func TestDefaultTriggers(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  script: [x]\n")
	want := sortedKinds(ir.TriggerPush, ir.TriggerTag)
	if got := kinds(pl); !reflect.DeepEqual(got, want) {
		t.Errorf("triggers = %v, want %v", got, want)
	}
}

func TestWorkflowTriggers(t *testing.T) {
	cases := map[string]struct {
		check func(*testing.T, *ir.Pipeline)
		rules string
		want  []ir.TriggerKind
	}{
		"mr":       {rules: `- if: $CI_PIPELINE_SOURCE == "merge_request_event"`, want: []ir.TriggerKind{ir.TriggerPR}},
		"schedule": {rules: `- if: $CI_PIPELINE_SOURCE == "schedule"`, want: []ir.TriggerKind{ir.TriggerSchedule}},
		"web":      {rules: `- if: $CI_PIPELINE_SOURCE == "web"`, want: []ir.TriggerKind{ir.TriggerManual}},
		"branch": {rules: `- if: $CI_COMMIT_BRANCH == "main"`, want: []ir.TriggerKind{ir.TriggerPush}, check: func(t *testing.T, pl *ir.Pipeline) {
			if got := trigger(t, pl, ir.TriggerPush).Branches.Include; !reflect.DeepEqual(got, []string{"main"}) {
				t.Errorf("branches = %v", got)
			}
		}},
		"two branches merge": {rules: "- if: $CI_COMMIT_BRANCH == \"main\"\n    - if: $CI_COMMIT_BRANCH == \"dev\"", want: []ir.TriggerKind{ir.TriggerPush}, check: func(t *testing.T, pl *ir.Pipeline) {
			if got := trigger(t, pl, ir.TriggerPush).Branches.Include; !reflect.DeepEqual(got, []string{"main", "dev"}) {
				t.Errorf("branches = %v", got)
			}
		}},
		"branch regex": {rules: `- if: $CI_COMMIT_BRANCH =~ /^release-.*$/`, want: []ir.TriggerKind{ir.TriggerPush}, check: func(t *testing.T, pl *ir.Pipeline) {
			if got := trigger(t, pl, ir.TriggerPush).Branches.Include; !reflect.DeepEqual(got, []string{"release-**"}) {
				t.Errorf("branches = %v", got)
			}
		}},
		"tag": {rules: `- if: $CI_COMMIT_TAG`, want: []ir.TriggerKind{ir.TriggerTag}},
		"tag regex": {rules: `- if: $CI_COMMIT_TAG =~ /^v/`, want: []ir.TriggerKind{ir.TriggerTag}, check: func(t *testing.T, pl *ir.Pipeline) {
			if got := trigger(t, pl, ir.TriggerTag).Tags.Include; !reflect.DeepEqual(got, []string{"v**"}) {
				t.Errorf("tags = %v", got)
			}
		}},
		"or":              {rules: `- if: $CI_PIPELINE_SOURCE == "merge_request_event" || $CI_COMMIT_BRANCH == "main"`, want: sortedKinds(ir.TriggerPush, ir.TriggerPR)},
		"catch-all never": {rules: "- if: $CI_COMMIT_BRANCH == \"main\"\n    - when: never", want: []ir.TriggerKind{ir.TriggerPush}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pl, rep := pipeline(t, "workflow:\n  rules:\n    "+c.rules+"\nj:\n  script: [x]\n")
			if got := kinds(pl); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("triggers = %v, want %v; report %+v", got, c.want, rep.Entries)
			}
			if ext(pl.Ext)["workflow"] == nil {
				t.Errorf("the workflow block must stay in Ext")
			}
			if c.check != nil {
				c.check(t, pl)
			}
		})
	}
}

func TestWorkflowScheduleReported(t *testing.T) {
	_, rep := pipeline(t, "workflow:\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"schedule\"\nj:\n  script: [x]\n")
	if !hasEntry(rep, "cron") {
		t.Errorf("report = %+v", rep.Entries)
	}
	for _, e := range rep.Entries {
		if e.Feature == "trigger.schedule" && e.Level.String() != "external" {
			t.Errorf("schedule level = %v", e.Level)
		}
	}
}

func TestWorkflowFallback(t *testing.T) {
	for name, rules := range map[string]string{
		"default branch":   `- if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH`,
		"and":              `- if: $CI_COMMIT_BRANCH && $CI_PIPELINE_SOURCE == "push"`,
		"never before":     "- if: $CI_COMMIT_BRANCH == \"x\"\n      when: never\n    - if: $CI_COMMIT_BRANCH",
		"unanchored regex": `- if: $CI_COMMIT_BRANCH =~ /release/`,
		"complex regex":    `- if: $CI_COMMIT_TAG =~ /^v\d+$/`,
		"changes":          `- changes: [a]`,
		"unknown source":   `- if: $CI_PIPELINE_SOURCE == "chat"`,
	} {
		t.Run(name, func(t *testing.T) {
			pl, rep := pipeline(t, "workflow:\n  rules:\n    "+rules+"\nj:\n  script: [x]\n")
			if got, want := kinds(pl), []ir.TriggerKind{ir.TriggerPush, ir.TriggerTag}; !reflect.DeepEqual(got, want) {
				t.Errorf("triggers = %v, want the defaults %v", got, want)
			}
			if ext(pl.Ext)["workflow"] == nil {
				t.Errorf("Ext workflow missing")
			}
			if !hasEntry(rep, "workflow:rules") {
				t.Errorf("report = %+v", rep.Entries)
			}
		})
	}
}
