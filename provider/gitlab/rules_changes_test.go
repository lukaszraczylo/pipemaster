package gitlab_test

import (
	"fmt"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
)

// glEvent is one GitLab pipeline: the predefined variables the rules read and the files the push or merge request changed.
type glEvent struct {
	name   string
	vars   map[string]string
	source string
	files  []string
	// unconditional is true when GitLab evaluates rules:changes to true without looking at files: tag, schedule
	// and other pipelines with no push event.
	unconditional bool
}

func (g glEvent) Lookup(path []string) (expr.Value, bool) {
	v, ok := g.vars[strings.Join(path, ".")]
	if !ok || v == "" {
		return nil, false
	}
	return v, true
}
func (g glEvent) Status() expr.Status { return expr.Success }

var glEvents = func() []glEvent {
	var out []glEvent
	base := []glEvent{
		{name: "push main", vars: map[string]string{"branch": "main", "event": "push"}},
		{name: "push dev", vars: map[string]string{"branch": "dev", "event": "push"}},
		{name: "merge request", vars: map[string]string{"event": "merge_request_event", "pr.number": "7"}},
		{name: "tag", vars: map[string]string{"tag": "v1", "event": "push"}, unconditional: true},
		{name: "schedule", vars: map[string]string{"branch": "main", "event": "schedule"}, unconditional: true},
	}
	for _, b := range base {
		for _, files := range [][]string{{"README.md"}, {"src/a.go"}} {
			e := b
			e.files = files
			e.name += " " + files[0]
			out = append(out, e)
		}
	}
	return out
}()

// matchGlob covers the two forms the cases use: a literal path and **/*.ext.
func matchGlob(glob, file string) bool {
	if ext, ok := strings.CutPrefix(glob, "**/*"); ok {
		return strings.HasSuffix(file, ext)
	}
	return glob == file
}

// changed is GitLab's rules:changes: true when any listed glob matches a changed file, and always true where
// the pipeline has no push event (docs.gitlab.com/ci/yaml/#ruleschanges).
func (g glEvent) changed(globs []string) bool {
	if g.unconditional {
		return true
	}
	for _, gl := range globs {
		for _, f := range g.files {
			if matchGlob(gl, f) {
				return true
			}
		}
	}
	return false
}

type refArm struct {
	If      string
	When    string
	Changes []string
}

func (g glEvent) armMatches(t *testing.T, a refArm) bool {
	t.Helper()
	if a.If != "" {
		e, err := exprgitlab.Parse(a.If)
		if err != nil {
			t.Fatal(err)
		}
		v, err := expr.Eval(e, g, exprgitlab.Semantics)
		if err != nil {
			t.Fatal(err)
		}
		if !exprgitlab.Semantics.Truthy(v) {
			return false
		}
	}
	return len(a.Changes) == 0 || g.changed(a.Changes)
}

// decide is GitLab's rules semantics: the first matching arm decides, when: never excludes, the default when is
// on_success, and a pipeline that the workflow rules refuse has no job at all.
func (g glEvent) decide(t *testing.T, workflow, rules []refArm) (run, manual bool) {
	t.Helper()
	if len(workflow) > 0 {
		admitted := false
		for _, a := range workflow {
			if g.armMatches(t, a) {
				admitted = a.When != "never"
				break
			}
		}
		if !admitted {
			return false, false
		}
	}
	for _, a := range rules {
		if !g.armMatches(t, a) {
			continue
		}
		switch a.When {
		case "never":
			return false, false
		case "manual":
			return true, true
		}
		return true, false
	}
	return false, false
}

func armsYAML(arms []refArm) string {
	var sb strings.Builder
	for _, a := range arms {
		first := true
		put := func(s string) {
			if first {
				sb.WriteString("    - " + s + "\n")
				first = false
				return
			}
			sb.WriteString("      " + s + "\n")
		}
		if a.If != "" {
			put("if: '" + a.If + "'")
		}
		if len(a.Changes) > 0 {
			put("changes:")
			for _, c := range a.Changes {
				sb.WriteString("        - '" + c + "'\n")
			}
		}
		if a.When != "" {
			put("when: " + a.When)
		}
	}
	return sb.String()
}

// condRuns evaluates an IR condition for one pipeline; a nil condition always holds.
func condRuns(t *testing.T, x expr.Expr, g glEvent) bool {
	t.Helper()
	if x == nil {
		return true
	}
	vals := map[string]bool{}
	for _, a := range expr.ChangesAtoms(x) {
		globs, _ := expr.ChangesGlobs(a)
		vals[expr.String(a)] = g.changed(globs)
	}
	v, err := expr.Eval(expr.AssignChanges(x, vals), g, exprgitlab.Semantics)
	if err != nil {
		t.Fatalf("eval %s: %v", expr.String(x), err)
	}
	return exprgitlab.Semantics.Truthy(v)
}

var (
	goPaths  = []string{"go.mod", "**/*.go"}
	mainOnly = `$CI_COMMIT_BRANCH == "main"`
	mrOnly   = `$CI_PIPELINE_SOURCE == "merge_request_event"`
)

func TestRulesFormsCompileToGitLabSemantics(t *testing.T) {
	cases := []struct {
		name     string
		workflow []refArm
		rules    []refArm
	}{
		{name: "if only", rules: []refArm{{If: mainOnly}}},
		{name: "if when never", rules: []refArm{{If: mrOnly, When: "never"}, {If: `$CI_PIPELINE_SOURCE == "push"`}}},
		{name: "if when manual", rules: []refArm{{If: mainOnly, When: "manual"}}},
		{name: "changes only", rules: []refArm{{Changes: goPaths}}},
		{name: "changes and if", rules: []refArm{{If: mainOnly, Changes: goPaths}}},
		{name: "two changes arms are an or", rules: []refArm{{Changes: goPaths}, {Changes: []string{"Makefile"}}}},
		{name: "never then changes", rules: []refArm{{If: `$CI_COMMIT_TAG`, When: "never"}, {If: mainOnly, Changes: goPaths}}},
		{name: "changes then catch-all never", rules: []refArm{{Changes: goPaths}, {When: "never"}}},
		{name: "mixed auto and manual arms", rules: []refArm{{If: mainOnly}, {If: mrOnly, When: "manual"}}},
		{name: "workflow rules anded", workflow: []refArm{{If: `$CI_COMMIT_TAG`, When: "never"}, {When: "always"}}, rules: []refArm{{If: mainOnly, Changes: goPaths}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sb strings.Builder
			if len(c.workflow) > 0 {
				sb.WriteString("workflow:\n  rules:\n" + armsYAML(c.workflow))
			}
			sb.WriteString("j:\n  script: [x]\n  rules:\n" + armsYAML(c.rules))
			pl, rep := pipeline(t, sb.String())
			j := job(t, pl, "j")
			if expr.HasOpaque(j.If) {
				t.Fatalf("rules must compile, got opaque %s; report %+v", expr.String(j.If), rep.Entries)
			}
			mixed := strings.Contains(c.name, "mixed")
			for _, g := range glEvents {
				wantRun, wantManual := g.decide(t, c.workflow, c.rules)
				if mixed && wantManual {
					wantRun = false // the manual arm is dropped and reported, never run by itself
				}
				if got := condRuns(t, j.If, g); got != wantRun {
					t.Errorf("%s: IR runs=%v, GitLab runs=%v (If = %s)", g.name, got, wantRun, expr.String(j.If))
				}
				if wantRun && j.Manual != wantManual {
					t.Errorf("%s: Manual=%v, want %v", g.name, j.Manual, wantManual)
				}
			}
			if mixed && !hasEntry(rep, "manual arm") {
				t.Errorf("a dropped manual arm needs a report entry: %+v", rep.Entries)
			}
		})
	}
}

func TestRulesChangesKeepSourceText(t *testing.T) {
	pl, _ := pipeline(t, "j:\n  script: [x]\n  rules:\n    - changes: [go.mod]\n")
	raw, _ := ext(job(t, pl, "j").Ext)["rules"].([]any)
	if len(raw) != 1 {
		t.Fatalf("the GitLab rules text must stay in Ext: %v", job(t, pl, "j").Ext)
	}
	if got := expr.String(job(t, pl, "j").If); got != "changes('go.mod')" {
		t.Errorf("If = %s", got)
	}
}

func TestRulesChangesFormsWithoutACanonicalMeaningStayOpaque(t *testing.T) {
	for name, rules := range map[string]string{
		"compare_to": "- changes:\n        paths: [go.mod]\n        compare_to: refs/heads/main",
		"variable":   `- changes: ['$DIR/**/*']`,
		"regexp":     "- changes:\n        regexp: 'x'",
	} {
		t.Run(name, func(t *testing.T) {
			pl, _ := pipeline(t, "j:\n  script: [x]\n  rules:\n    "+rules+"\n")
			if _, ok := job(t, pl, "j").If.(expr.Opaque); !ok {
				t.Fatalf("If = %s, want opaque", expr.String(job(t, pl, "j").If))
			}
		})
	}
}

func TestWorkflowRulesWithPullRequestBecomeTriggers(t *testing.T) {
	pl, _ := pipeline(t, `workflow:
  rules:
    - if: $CI_MERGE_REQUEST_IID
    - if: '$CI_COMMIT_BRANCH == "main"'
    - if: $CI_COMMIT_TAG
j:
  script: [x]
`)
	var kinds []string
	for _, tr := range pl.Triggers {
		kinds = append(kinds, string(tr.Kind))
	}
	if got := strings.Join(kinds, ","); got != "pr,push,tag" {
		t.Errorf("triggers = %s, want pr,push,tag", got)
	}
	if job(t, pl, "j").If != nil {
		t.Errorf("exact triggers need no job guard, got %s", expr.String(job(t, pl, "j").If))
	}
}

func TestJobRulesEqualToWorkflowRulesAddNothing(t *testing.T) {
	pl, _ := pipeline(t, `workflow:
  rules: &w
    - if: $CI_MERGE_REQUEST_IID
    - if: $CI_COMMIT_TAG
j:
  script: [x]
  rules: *w
`)
	if x := job(t, pl, "j").If; x != nil {
		t.Errorf("a job whose rules are the workflow rules runs whenever the pipeline exists, got %s", expr.String(x))
	}
	if ext(job(t, pl, "j").Ext)["rules"] == nil {
		t.Errorf("the rules text must stay in Ext")
	}
}

var _ = fmt.Sprint
var _ yaml.Node
