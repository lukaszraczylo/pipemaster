package gitlab_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/report"
)

const childJob = "t-j:\n  script: [echo hi]\n"

func triggerFile(trigger string) string {
	return "call:\n  trigger:\n" + trigger + "\n"
}

func callJob(t *testing.T, p *ir.Project) *ir.Job {
	t.Helper()
	return job(t, p.Pipelines[0], "call")
}

func TestChildIncludeForms(t *testing.T) {
	two := "other:\n  script: [echo other]\n"
	for _, tc := range []struct {
		name    string
		trigger string
		files   []string
		jobs    []string
	}{
		{"string", "    include: ci/child.yml", []string{"ci/child.yml"}, []string{"t-j"}},
		{"local map", "    include:\n      - local: ci/child.yml", []string{"ci/child.yml"}, []string{"t-j"}},
		{"list of strings", "    include:\n      - ci/child.yml\n      - ci/two.yml", []string{"ci/child.yml", "ci/two.yml"}, []string{"other", "t-j"}},
		{"leading slash", "    include:\n      - local: /ci/child.yml", []string{"ci/child.yml"}, []string{"t-j"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := parseFiles(t, map[string]string{rootFile: triggerFile(tc.trigger), "ci/child.yml": childJob, "ci/two.yml": two})
			c := callJob(t, p).Child
			if c == nil || c.Pipeline == nil {
				t.Fatalf("no child pipeline on the call job")
			}
			if !slices.Equal(c.Files, tc.files) {
				t.Errorf("files = %v, want %v", c.Files, tc.files)
			}
			var got []string
			for _, j := range c.Pipeline.Jobs {
				got = append(got, j.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.jobs) {
				t.Errorf("child jobs = %v, want %v", got, tc.jobs)
			}
			if !slices.Contains(p.Origin.Files, "ci/child.yml") {
				t.Errorf("origin files = %v, want the child file", p.Origin.Files)
			}
		})
	}
}

func TestChildKeepsFailLoudForms(t *testing.T) {
	for _, tc := range []struct{ name, trigger string }{
		{"missing file", "    include: ci/none.yml"},
		{"artifact", "    include:\n      - artifact: gen.yml\n        job: gen"},
		{"project file", "    include:\n      - project: g/p\n        file: ci.yml"},
		{"template", "    include:\n      - template: Auto-DevOps.gitlab-ci.yml"},
		{"remote", "    include:\n      - remote: https://example.com/ci.yml"},
		{"trigger project", "    project: g/other\n    branch: main"},
		{"glob", "    include: ci/*.yml"},
		{"parent path", "    include: ../ci/child.yml"},
		{"include with rules", "    include:\n      - local: ci/child.yml\n        rules:\n          - if: $A"},
		{"mixed local and remote", "    include:\n      - local: ci/child.yml\n      - remote: https://example.com/ci.yml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, rep := parseFiles(t, map[string]string{rootFile: triggerFile(tc.trigger), "ci/child.yml": childJob})
			j := callJob(t, p)
			if j.Child != nil {
				t.Fatalf("child = %+v, want none: the trigger must keep its fail-loud form", j.Child)
			}
			if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
				return e.IRPath == "pipeline/gitlab-ci/job/call" && e.Level == caps.Lost
			}) {
				t.Errorf("no lost entry names the trigger job: %v", rep.Entries)
			}
		})
	}
}

func TestChildCycleStops(t *testing.T) {
	again := "again:\n  trigger:\n    include: ci/child.yml\n"
	p, rep := parseFiles(t, map[string]string{rootFile: triggerFile("    include: ci/child.yml"), "ci/child.yml": again})
	c := callJob(t, p).Child
	if c == nil {
		t.Fatal("the first child must still attach")
	}
	if inner := job(t, c.Pipeline, "again"); inner.Child != nil {
		t.Errorf("a child that triggers itself must not nest: %+v", inner.Child)
	}
	if !slices.ContainsFunc(rep.Entries, func(e report.Entry) bool {
		return e.Level == caps.Lost && strings.Contains(e.Detail, "without end")
	}) {
		t.Errorf("no lost entry names the cycle: %v", rep.Entries)
	}
}

func TestChildWorkflowRulesGateEveryJob(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		want       string
		never      bool
	}{
		{"branch test", `$CI_COMMIT_BRANCH == "main"`, "branch", false},
		{"push source", `$CI_PIPELINE_SOURCE == "push"`, "", true},
		{"parent source", `$CI_PIPELINE_SOURCE == "parent_pipeline"`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := "workflow:\n  rules:\n    - if: '" + tc.rule + "'\n" + childJob
			p, rep := parseFiles(t, map[string]string{rootFile: triggerFile("    include: ci/child.yml"), "ci/child.yml": child})
			c := callJob(t, p).Child
			if c == nil {
				t.Fatalf("no child; report %v", rep.Entries)
			}
			cond := andGate(c.Gate, job(t, c.Pipeline, "t-j").If)
			text := ""
			if cond != nil {
				text = expr.String(cond)
			}
			switch {
			case tc.never:
				if folded := expr.FoldEvent(cond, "parent_pipeline"); expr.String(folded) != "false" {
					t.Errorf("the child never starts from a parent: condition %q must fold to false, got %q", text, expr.String(folded))
				}
			case tc.want != "":
				if !strings.Contains(text, tc.want) {
					t.Errorf("condition %q must test the %s of the rule", text, tc.want)
				}
			}
		})
	}
}

func andGate(a, b expr.Expr) expr.Expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return expr.Binary{Op: expr.OpAnd, L: a, R: b}
}
