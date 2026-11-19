package gitlab_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

func triggerProject(ts ...ir.Trigger) *ir.Project {
	return &ir.Project{Pipelines: []*ir.Pipeline{{
		ID:       "gitlab-ci",
		Triggers: ts,
		Jobs:     []*ir.Job{{ID: "build", Steps: []*ir.Step{{ID: "s", Kind: ir.StepScript, Script: []string{"make"}}}}},
	}}}
}

func TestNegatedFilterEntriesBecomeExclusions(t *testing.T) {
	tests := []struct {
		name string
		trig ir.Trigger
		want []string
		not  []string
	}{
		{"branch", ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"release/**", "!release/old"}}},
			[]string{`$CI_COMMIT_BRANCH =~ /^release\/.*$/ && $CI_COMMIT_BRANCH != "release/old"`}, []string{`== "!`}},
		{"pr base", ir.Trigger{Kind: ir.TriggerPR, Branches: ir.Filter{Include: []string{"main", "!main-old"}}},
			[]string{`!= "main-old"`}, []string{`== "!`}},
		{"tag", ir.Trigger{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*", "!v0*"}}},
			[]string{`$CI_COMMIT_TAG =~ /^v[^\/]*$/ && $CI_COMMIT_TAG !~ /^v0[^\/]*$/`}, []string{`=~ /^!`}},
		{"reinclude", ir.Trigger{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"a*", "!ab", "abc"}}},
			[]string{`!= "ab"`, `== "abc"`}, []string{`== "!`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := emitDoc(t, triggerProject(tc.trig))
			for _, w := range tc.want {
				if !strings.Contains(doc, w) {
					t.Errorf("missing %q in:\n%s", w, doc)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(doc, n) {
					t.Errorf("unexpected %q in:\n%s", n, doc)
				}
			}
		})
	}
}

func TestNegatedPathFilterIsReportedNotWritten(t *testing.T) {
	p := triggerProject(ir.Trigger{Kind: ir.TriggerPush, Paths: ir.Filter{Include: []string{"src/**", "!src/gen/**"}}})
	files, rep := emitFiles(t, p)
	doc := string(files[rootFile])
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "- '!") || strings.HasPrefix(strings.TrimSpace(l), "- \"!") || strings.HasPrefix(strings.TrimSpace(l), "- !") {
			t.Errorf("rules:changes carries a negated pattern %q:\n%s", l, doc)
		}
	}
	if !strings.Contains(doc, "src/**") {
		t.Errorf("positive path dropped:\n%s", doc)
	}
	if !hasEntry(rep, "rules:changes has no exclude patterns") {
		t.Errorf("no lost entry for the excluded path: %+v", rep.Entries)
	}
}
