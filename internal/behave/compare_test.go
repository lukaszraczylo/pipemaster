//go:build behave

package behave

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/report"
)

func rep(entries ...report.Entry) *report.Report { return &report.Report{Entries: entries} }

func entry(l caps.Level, f caps.Feature, path string) report.Entry {
	return report.Entry{Level: l, Feature: f, IRPath: path}
}

func TestJudge(t *testing.T) {
	fx := Fixture{Owners: map[string][]string{
		"branch.main": {"main-only"}, "branch.tag": {"tag-only"}, "branch.always": {"always"},
	}}
	tr := func(exit int, m ...string) *Trace { return &Trace{Markers: m, Exit: exit} }
	cases := []struct {
		src    *Trace
		tgt    *Trace
		rep    *report.Report
		name   string
		log    string
		status string
	}{
		{name: "same", src: tr(0, "a"), tgt: tr(0, "a"), status: StatusPass},
		{name: "lost condition adds work", src: tr(0, "branch.always"), tgt: tr(0, "branch.always", "branch.tag"),
			rep: rep(entry(caps.Lost, caps.JobIf, "pipeline/ci/job/tag-only")), status: StatusExplained},
		{name: "lost condition cannot remove work", src: tr(0, "branch.always", "branch.tag"), tgt: tr(0, "branch.always"),
			rep: rep(entry(caps.Lost, caps.StepIf, "pipeline/d/job/default/step/tag-only")), status: StatusFail},
		{name: "external condition disables owned work", src: tr(0, "branch.always", "branch.tag"), tgt: tr(0, "branch.always"),
			rep: rep(entry(caps.External, caps.JobIf, "pipeline/ci/job/tag-only")), status: StatusExplained},
		{name: "entry of another job does not explain", src: tr(0, "branch.always", "branch.tag"), tgt: tr(0, "branch.always"),
			rep: rep(entry(caps.External, caps.JobIf, "pipeline/ci/job/main-only")), status: StatusFail},
		{name: "inert feature explains nothing", src: tr(0, "a"), tgt: tr(0, "b"),
			rep: rep(entry(caps.Lost, caps.DisplayName, "pipeline/ci")), status: StatusFail},
		{name: "value change by lost shell", src: tr(0, "bash.ok:c"), tgt: tr(0, "bash.ok:"),
			rep: rep(entry(caps.Lost, caps.StepShell, "pipeline/ci/job/build/step/s")), status: StatusExplained},
		{name: "lost runner label cannot change a value", src: tr(0, "workspace.s2:data"), tgt: tr(0, "workspace.s2:"),
			rep: rep(entry(caps.Lost, caps.JobRunner, "pipeline/ci/job/build")), status: StatusFail},
		{name: "external runner label explains a job that never starts", src: tr(0, "a", "b"), tgt: tr(1, "a"),
			rep: rep(entry(caps.External, caps.JobRunner, "pipeline/ci/job/b")), status: StatusExplained},
		{name: "exit alone without fact", src: tr(0, "a"), tgt: tr(2, "a"),
			rep: rep(entry(caps.Lost, caps.StepShell, "pipeline/ci/job/build/step/s")), status: StatusFail},
		{name: "fail loud explains missing and exit", src: tr(0, "a", "b"), tgt: tr(1, "a"),
			rep:    rep(entry(caps.Lost, caps.StepAction, "pipeline/ci/job/x/step/y")),
			log:    failLoudMark + "step.action pipeline/ci/job/x/step/y: no form",
			status: StatusExplained},
		{name: "exit code is exact", src: tr(1, "a"), tgt: tr(2, "a"), status: StatusFail},
		{name: "emulated order change", src: tr(0, "x.flaky", "x.after"), tgt: tr(0, "x.after", "x.flaky"),
			rep: rep(entry(caps.Emulated, caps.JobAllowFailure, "pipeline/ci/job/flaky")), status: StatusExplained},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.tgt.Log = c.log
			got, detail, rules := Judge(fx, c.src, c.tgt, c.rep)
			if got != c.status {
				t.Fatalf("status %s, want %s (%s | %s)", got, c.status, detail, rules)
			}
			if got == StatusExplained && !strings.Contains(rules, "explains") {
				t.Fatalf("rule not printed: %q", rules)
			}
		})
	}
}

func TestTopoOrder(t *testing.T) {
	for _, c := range []struct {
		name  string
		names []string
		deps  map[string][]string
		want  []string
	}{
		{"dependency first", []string{"after", "flaky"}, map[string][]string{"after": {"flaky"}}, []string{"flaky", "after"}},
		{"keeps order", []string{"a", "b", "c"}, nil, []string{"a", "b", "c"}},
		{"unknown dependency ignored", []string{"a"}, map[string][]string{"a": {"zzz"}}, []string{"a"}},
		{"cycle keeps every name once", []string{"a", "b"}, map[string][]string{"a": {"b"}, "b": {"a"}}, []string{"b", "a"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := topoOrder(c.names, c.deps); !slices.Equal(got, c.want) {
				t.Errorf("topoOrder = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHasVerdictNeedsAReportedEntry(t *testing.T) {
	if hasVerdict(nil, caps.TriggerPullRequest) {
		t.Fatal("a missing report holds no verdict")
	}
	if hasVerdict(rep(entry(caps.Lost, caps.TriggerPush, "pipeline/ci/trigger/0")), caps.TriggerPullRequest) {
		t.Fatal("an entry for another trigger is no verdict")
	}
	if !hasVerdict(rep(entry(caps.Lost, caps.TriggerPullRequest, "pipeline/ci/trigger/0")), caps.TriggerPullRequest) {
		t.Fatal("a lost pull request entry is the verdict")
	}
}
