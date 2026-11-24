package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
)

const (
	condURI  = "https://example.com/r.git"
	branches = "- {name: branches, type: git, source: {uri: " + condURI + ", version_type: branches, branch_filters: [feat/*, feature/*]}}\n"
	repoDef  = "- {name: repo, type: git, source: {uri: " + condURI + "}}\n"
	// multiBranch is the shape of corpus 87: one default-branch resource, one branch-set resource, a chained job.
	multiBranch = "resources:\n" + repoDef + branches +
		"jobs:\n- name: set-feature\n  plan:\n  - {get: branches, trigger: true}\n" + task +
		"- name: test\n  plan:\n  - {get: repo, trigger: true}\n" + task +
		"- name: build\n  plan:\n  - {get: repo, passed: [test], trigger: true}\n" + task
)

// condEnv is a run of a converted pipeline: the event, the ref and the default branch of the repository.
type condEnv map[string]string

func (c condEnv) Lookup(path []string) (expr.Value, bool) {
	v, ok := c[strings.Join(path, ".")]
	return v, ok
}

func (c condEnv) Status() expr.Status { return expr.Success }

func runs(t *testing.T, j *ir.Job, env condEnv) bool {
	t.Helper()
	if j.If == nil {
		return !j.Manual
	}
	v, err := expr.Eval(j.If, env, exprconcourse.Semantics)
	if err != nil {
		t.Fatalf("Eval %s: %v", expr.String(j.If), err)
	}
	b, _ := v.(bool)
	return b
}

func TestJobsKeepTheEventsOfTheirOwnGets(t *testing.T) {
	pl, _ := parseYAML(t, multiBranch)
	for _, id := range []string{"set-feature", "test"} {
		j := job(t, pl, id)
		if j.Manual || j.If == nil {
			t.Errorf("%s must run on its own push events, got manual=%v if=%q", id, j.Manual, cond(j.If))
		}
	}
	if b, tst := job(t, pl, "build"), job(t, pl, "test"); b.Manual || cond(b.If) != cond(tst.If) {
		t.Errorf("build follows test and takes its condition, so that it never needs a job that does not run: manual=%v if=%q", b.Manual, cond(b.If))
	}
}

// TestNoJobRunsOnAnEventItsResourcesDoNotWatch is the fail-safe table: each row is a run, each column a job.
func TestNoJobRunsOnAnEventItsResourcesDoNotWatch(t *testing.T) {
	pl, _ := parseYAML(t, multiBranch)
	set, test, build := job(t, pl, "set-feature"), job(t, pl, "test"), job(t, pl, "build")
	const def = "main"
	for _, c := range []struct {
		env       condEnv
		name      string
		set, test bool
	}{
		{condEnv{"event": "push", "branch": "main", "default_branch": def}, "push to the default branch", false, true},
		{condEnv{"event": "push", "branch": "feat/x", "default_branch": def}, "push to feat/x", true, false},
		{condEnv{"event": "push", "branch": "feature/y", "default_branch": def}, "push to feature/y", true, false},
		{condEnv{"event": "push", "branch": "dev", "default_branch": def}, "push to another branch", false, false},
		{condEnv{"event": "push", "branch": "feature", "default_branch": def}, "push to a branch that only starts like a filter", false, false},
		{condEnv{"event": "push", "tag": "v1", "default_branch": def}, "tag push", false, false},
		{condEnv{"event": "push", "tag": "main", "default_branch": def}, "tag named like the default branch", false, false},
		{condEnv{"event": "pull_request", "branch": "main", "default_branch": def}, "pull request on the default branch", false, false},
		{condEnv{"event": "pull_request", "branch": "feat/x", "default_branch": def}, "pull request on feat/x", false, false},
		{condEnv{"event": "schedule", "branch": "main", "default_branch": def}, "schedule on the default branch", false, false},
		{condEnv{"event": "workflow_dispatch", "branch": "main", "default_branch": def}, "manual start on the default branch", false, false},
	} {
		if got := runs(t, set, c.env); got != c.set {
			t.Errorf("%s: set-feature runs=%v, want %v (if %s)", c.name, got, c.set, cond(set.If))
		}
		if got := runs(t, test, c.env); got != c.test {
			t.Errorf("%s: test runs=%v, want %v (if %s)", c.name, got, c.test, cond(test.If))
		}
		if got := runs(t, build, c.env); got != c.test {
			t.Errorf("%s: build runs=%v, want %v: it follows test (if %s)", c.name, got, c.test, cond(build.If))
		}
	}
}

func TestDefaultBranchTriggerIsReported(t *testing.T) {
	_, rep := parseYAML(t, multiBranch)
	if !hasEntry(rep, "default branch") || !hasEntry(rep, "branch_filters") {
		t.Errorf("entries must name the default branch and the branch filters: %v", rep.Entries)
	}
	for _, e := range rep.Entries {
		if strings.Contains(e.Detail, "never fires") {
			t.Errorf("a readable trigger became a never-firing one: %s", e.Detail)
		}
	}
}

func TestConditionReachesGitLabRules(t *testing.T) {
	proj, _ := parseFiles(t, map[string]string{"ci.yml": multiBranch})
	out, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	body := string(out[".gitlab-ci.yml"])
	for _, want := range []string{"$CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH", `feat\/`, `feature\/`} {
		if !strings.Contains(body, want) {
			t.Errorf("GitLab output lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "when: manual") {
		t.Errorf("no job is manual:\n%s", body)
	}
}

func TestConditionReachesGHAIf(t *testing.T) {
	proj, _ := parseFiles(t, map[string]string{"ci.yml": multiBranch})
	out, _, err := gha.New(gha.GHA).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for name, b := range out {
		if strings.HasSuffix(name, ".yml") {
			body += string(b)
		}
	}
	for _, want := range []string{"github.event.repository.default_branch", "github.ref_type == 'branch'", "startsWith(github.ref_name, 'feat/')"} {
		if !strings.Contains(body, want) {
			t.Errorf("GHA output lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "workflow_dispatch'") {
		t.Errorf("no job is manual:\n%s", body)
	}
}

func TestOwnBranchGetBecomesPushToThatBranch(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n- {name: main-src, type: git, source: {uri: u, branch: main}}\n- {name: dev-src, type: git, source: {uri: u, branch: dev}}\n"+
		"jobs:\n- name: a\n  plan:\n  - {get: main-src, trigger: true}\n"+task+"- name: b\n  plan:\n  - {get: dev-src, trigger: true}\n"+task)
	for id, br := range map[string]string{"a": "main", "b": "dev"} {
		j := job(t, pl, id)
		if j.Manual {
			t.Errorf("%s must not be manual", id)
		}
		if !runs(t, j, condEnv{"event": "push", "branch": br}) || runs(t, j, condEnv{"event": "push", "branch": "other"}) || runs(t, j, condEnv{"event": "pull_request", "branch": br}) {
			t.Errorf("%s: wrong events for if %s", id, cond(j.If))
		}
	}
}

func TestUnreadableOrUnsafeRootsStayManualOrNeverFire(t *testing.T) {
	for name, y := range map[string]string{
		"glob with a middle star": "resources:\n- {name: b, type: git, source: {uri: u, version_type: branches, branch_filters: ['a/*/z']}}\n",
		"filter with a variable":  "resources:\n- {name: b, type: git, source: {uri: u, version_type: branches, branch_filters: ['((p))/*']}}\n",
		"filters with no list":    "resources:\n- {name: b, type: git, source: {uri: u, version_type: branches}}\n",
		"other version type":      "resources:\n- {name: b, type: git, source: {uri: u, version_type: tags, branch_filters: [x]}}\n",
	} {
		pl, _ := parseYAML(t, y+"- {name: other, type: git, source: {uri: u2, branch: x}}\njobs:\n- name: j\n  plan:\n  - {get: b, trigger: true}\n"+task)
		j := job(t, pl, "j")
		if !j.Manual && j.If != nil && runs(t, j, condEnv{"event": "push", "branch": "a/q/z"}) {
			t.Errorf("%s: the job must not run on a push it cannot read: if %s", name, cond(j.If))
		}
		for _, tr := range pl.Triggers {
			if tr.Kind == ir.TriggerPush && len(tr.Branches.Include) == 0 && j.If == nil {
				t.Errorf("%s: an unfiltered push trigger with no job condition", name)
			}
		}
	}
}

func TestPathFiltersOnAJobRootKeepTheJobManual(t *testing.T) {
	y := "resources:\n- {name: src, type: git, source: {uri: u, branch: main, paths: [app/]}}\n- {name: tick, type: time, source: {interval: 24h}}\n" +
		"jobs:\n- name: a\n  plan:\n  - {get: src, trigger: true}\n" + task + "- name: b\n  plan:\n  - {get: tick, trigger: true}\n" + task
	pl, _ := parseYAML(t, y)
	if j := job(t, pl, "a"); !j.Manual {
		t.Errorf("a path filter has no job condition: a must stay manual, if %s", cond(j.If))
	}
}

func TestTimeAndTagRootsKeepTheirVerdict(t *testing.T) {
	pl, _ := parseYAML(t, threeRoots)
	for _, id := range []string{"nightly-job", "rel"} {
		if !job(t, pl, id).Manual {
			t.Errorf("%s must stay manual", id)
		}
	}
	if j := job(t, pl, "build"); j.Manual || j.If == nil {
		t.Errorf("build triggers on a push to main only: manual=%v if=%s", j.Manual, cond(j.If))
	}
}

func TestDefaultBranchRootWidensNoJob(t *testing.T) {
	pl, _ := parseYAML(t, "resources:\n"+repoDef+"- {name: tick, type: time, source: {interval: 24h}}\njobs:\n- name: both\n  plan:\n  - {get: repo, trigger: true}\n  - {get: tick, trigger: true}\n"+task+
		"- name: only\n  plan:\n  - {get: repo, trigger: true}\n"+task)
	both := job(t, pl, "both")
	if !both.Manual && both.If == nil {
		t.Errorf("a job on a default-branch push and a schedule must not run on every push: manual=%v", both.Manual)
	}
	if runs(t, both, condEnv{"event": "push", "branch": "dev", "default_branch": "main"}) {
		t.Errorf("both runs on a push to dev: if %s", cond(both.If))
	}
}
