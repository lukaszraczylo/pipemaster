package concourse_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
	exprgha "github.com/lukaszraczylo/pipemaster/expr/gha"
	exprgitlab "github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
	"github.com/lukaszraczylo/pipemaster/provider/gitlab"
	"github.com/lukaszraczylo/pipemaster/validate"
	"go.yaml.in/yaml/v3"
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
type condEnv map[string]any

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
	const def, zero, old = "main", "0000000000000000000000000000000000000000", "1111111111111111111111111111111111111111"
	push := func(branch, before string) condEnv {
		return condEnv{"event": "push", "branch": branch, "before_sha": before, "default_branch": def}
	}
	for _, c := range []struct {
		env       condEnv
		name      string
		set, test bool
	}{
		{push("main", old), "push to the default branch", false, true},
		{push("main", zero), "push that creates the default branch", false, true},
		{push("feat/x", zero), "push that creates feat/x", true, false},
		{push("feature/y", zero), "push that creates feature/y", true, false},
		{push("feat/a/b", zero), "push that creates feat/a/b: * also matches /", true, false},
		{push("feat/x", old), "push to the existing branch feat/x: the branch set does not change", false, false},
		{push("feature/y", old), "push to the existing branch feature/y", false, false},
		{push("Feat/x", zero), "creation of Feat/x: the filter is case-sensitive", false, false},
		{push("dev", zero), "creation of another branch", false, false},
		{push("feature", zero), "creation of a branch that only starts like a filter", false, false},
		{condEnv{"event": "push", "tag": "feat/x", "before_sha": zero, "default_branch": def}, "creation of a tag named like a branch", false, false},
		{condEnv{"event": "delete", "branch": "feat/x", "default_branch": def}, "deletion of feat/x has no push event", false, false},
		{condEnv{"event": "create", "branch": "feat/x", "default_branch": def}, "create event of feat/x", false, false},
		{condEnv{"event": "push", "tag": "v1", "before_sha": old, "default_branch": def}, "tag push", false, false},
		{condEnv{"event": "push", "tag": "main", "before_sha": zero, "default_branch": def}, "tag named like the default branch", false, false},
		{condEnv{"event": "pull_request", "branch": "main", "before_sha": zero, "default_branch": def}, "pull request on the default branch", false, false},
		{condEnv{"event": "pull_request", "branch": "feat/x", "before_sha": zero, "default_branch": def}, "pull request on feat/x", false, false},
		{condEnv{"event": "schedule", "branch": "main", "before_sha": zero, "default_branch": def}, "schedule on the default branch", false, false},
		{condEnv{"event": "workflow_dispatch", "branch": "feat/x", "before_sha": zero, "default_branch": def}, "manual start on feat/x", false, false},
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
	for _, want := range []string{"$CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH", `$CI_COMMIT_BEFORE_SHA == "0000000000000000000000000000000000000000"`, `=~ /^feat\//`, `=~ /^feature\//`} {
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
	for _, want := range []string{"github.event.repository.default_branch", "github.ref_type == 'branch'"} {
		if !strings.Contains(body, want) {
			t.Errorf("GHA output lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "workflow_dispatch'") {
		t.Errorf("no job is manual:\n%s", body)
	}
	// GHA has no case-sensitive prefix test, so the branch-set job is disabled rather than run on more events.
	if strings.Contains(body, "startsWith") || !strings.Contains(body, "if: false") || !strings.Contains(body, "lost job.if") {
		t.Errorf("the branch-set job must be disabled with a lost entry:\n%s", body)
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

// target payloads: the conditions that the GHA and GitLab files carry, evaluated on the values each provider sets.

func ghaFile(t *testing.T, y string) map[string]any {
	t.Helper()
	proj, _ := parseFiles(t, map[string]string{"ci.yml": y})
	out, _, err := gha.New(gha.GHA).Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range out {
		if strings.HasSuffix(name, ".yml") {
			var doc map[string]any
			if err := yaml.Unmarshal(b, &doc); err != nil {
				t.Fatal(err)
			}
			return doc
		}
	}
	t.Fatal("no GHA workflow written")
	return nil
}

// ghaRuns evaluates the if of a job on a GHA payload; a job with no if runs.
func ghaRuns(t *testing.T, doc map[string]any, id string, env condEnv) bool {
	t.Helper()
	jobs, _ := doc["jobs"].(map[string]any)
	j, _ := jobs[id].(map[string]any)
	if j == nil {
		t.Fatalf("GHA job %s missing", id)
	}
	raw, has := j["if"]
	if !has {
		return true
	}
	x, err := exprgha.Parse(fmt.Sprint(raw))
	if err != nil {
		t.Fatalf("parse GHA if %v: %v", raw, err)
	}
	v, err := expr.Eval(x, env, exprgha.Semantics)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := v.(bool)
	return b
}

func gitlabFile(t *testing.T, y string) (map[string]any, []byte) {
	t.Helper()
	proj, _ := parseFiles(t, map[string]string{"ci.yml": y})
	out, _, err := gitlab.New().Emit(proj)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out[".gitlab-ci.yml"], &doc); err != nil {
		t.Fatal(err)
	}
	return doc, out[".gitlab-ci.yml"]
}

// gitlabRuns is true when a rule of the job holds on the variables of a pipeline that GitLab creates.
func gitlabRuns(t *testing.T, doc map[string]any, id string, env condEnv) bool {
	t.Helper()
	j, _ := doc[id].(map[string]any)
	if j == nil {
		t.Fatalf("GitLab job %s missing", id)
	}
	rules, _ := j["rules"].([]any)
	if len(rules) == 0 {
		return j["when"] != "manual"
	}
	for _, r := range rules {
		rm, _ := r.(map[string]any)
		holds := true
		if c, has := rm["if"]; has {
			x, err := exprgitlab.Parse(fmt.Sprint(c))
			if err != nil {
				t.Fatalf("parse GitLab rule %v: %v", c, err)
			}
			v, err := expr.Eval(x, env, exprgitlab.Semantics)
			if err != nil {
				t.Fatal(err)
			}
			holds, _ = v.(bool)
		}
		if holds {
			return rm["when"] != "never"
		}
	}
	return false
}

func TestTargetPayloadsNeverRunAJobOnAWrongEvent(t *testing.T) {
	const y = multiBranch
	gh := ghaFile(t, y)
	gl, _ := gitlabFile(t, y)
	const zero, old = "0000000000000000000000000000000000000000", "1111111111111111111111111111111111111111"
	ghPush := func(branch, before string, deleted bool) condEnv {
		return condEnv{"event": "push", "ref_type": "branch", "ref_name": branch, "created": before == zero, "deleted": deleted, "default_branch": "main"}
	}
	glPush := func(branch, before string) condEnv {
		return condEnv{"event": "push", "branch": branch, "before_sha": before, "default_branch": "main"}
	}
	for _, c := range []struct {
		gh, gl    condEnv // nil: the provider starts no pipeline
		name      string
		test, set bool
	}{
		{ghPush("main", old, false), glPush("main", old), "push to the default branch", true, false},
		{ghPush("main", old, true), nil, "deletion push of the default branch: GHA fires a push, GitLab no pipeline", false, false},
		{ghPush("feat/x", zero, true), nil, "deletion push of feat/x", false, false},
		{ghPush("feat/x", zero, false), glPush("feat/x", zero), "creation of feat/x", false, true},
		{ghPush("feat/x", old, false), glPush("feat/x", old), "push to feat/x", false, false},
		{condEnv{"event": "push", "ref_type": "tag", "ref_name": "main", "created": true, "deleted": false, "default_branch": "main"}, condEnv{"event": "push", "tag": "main", "before_sha": zero, "default_branch": "main"}, "tag named like the default branch", false, false},
		{condEnv{"event": "pull_request", "ref_type": "branch", "ref_name": "1/merge", "default_branch": "main"}, condEnv{"event": "merge_request_event", "branch": "", "default_branch": "main"}, "pull request", false, false},
		{condEnv{"event": "schedule", "ref_type": "branch", "ref_name": "main", "default_branch": "main"}, condEnv{"event": "schedule", "branch": "main", "before_sha": zero, "default_branch": "main"}, "schedule on the default branch", false, false},
		{condEnv{"event": "workflow_dispatch", "ref_type": "branch", "ref_name": "main", "default_branch": "main"}, condEnv{"event": "web", "branch": "main", "before_sha": zero, "default_branch": "main"}, "manual start on the default branch", false, false},
	} {
		if got := ghaRuns(t, gh, "test", c.gh); got != c.test {
			t.Errorf("GHA %s: test runs=%v, want %v", c.name, got, c.test)
		}
		if got := ghaRuns(t, gh, "set-feature", c.gh); got {
			t.Errorf("GHA %s: set-feature is disabled on GHA and must not run", c.name)
		}
		if c.gl == nil {
			continue
		}
		if got := gitlabRuns(t, gl, "test", c.gl); got != c.test {
			t.Errorf("GitLab %s: test runs=%v, want %v", c.name, got, c.test)
		}
		if got := gitlabRuns(t, gl, "set-feature", c.gl); got != c.set {
			t.Errorf("GitLab %s: set-feature runs=%v, want %v", c.name, got, c.set)
		}
	}
}

func TestExactBranchJobIgnoresADeletionPushOnGHA(t *testing.T) {
	doc := ghaFile(t, "resources:\n- {name: src, type: git, source: {uri: u, branch: dev}}\n- {name: tick, type: time, source: {interval: 24h}}\njobs:\n- name: a\n  plan:\n  - {get: src, trigger: true}\n"+task+"- name: b\n  plan:\n  - {get: tick, trigger: true}\n"+task)
	env := func(deleted bool) condEnv {
		return condEnv{"event": "push", "ref_type": "branch", "ref_name": "dev", "deleted": deleted}
	}
	if !ghaRuns(t, doc, "a", env(false)) || ghaRuns(t, doc, "a", env(true)) {
		t.Error("a must run on a push to dev and not on its deletion")
	}
}

const needsProbe = "resources:\n- {name: src, type: git, source: {uri: u, branch: main}}\n- {name: other, type: git, source: {uri: u, branch: dev}}\njobs:\n" +
	"- name: test\n  plan:\n  - {get: src, trigger: true}\n" + task +
	"- name: nodtrig\n  plan:\n  - {get: src, passed: [test]}\n" + task +
	"- name: both\n  plan:\n  - {get: other, trigger: true}\n  - {get: src, passed: [test], trigger: true}\n" + task

func TestJobsThatNeedAConditionedJobKeepTheNeedOptional(t *testing.T) {
	pl, rep := parseYAML(t, needsProbe)
	for _, id := range []string{"nodtrig", "both"} {
		j := job(t, pl, id)
		if len(j.Needs) != 1 || !j.Needs[0].Optional {
			t.Errorf("%s: need on test must be optional: %+v", id, j.Needs)
		}
	}
	if !hasEntry(rep, "optional") {
		t.Error("no entry names the optional need")
	}
	doc, body := gitlabFile(t, needsProbe)
	for _, id := range []string{"nodtrig", "both"} {
		j, _ := doc[id].(map[string]any)
		needs, _ := j["needs"].([]any)
		nm, _ := needs[0].(map[string]any)
		if len(needs) != 1 || nm["optional"] != true {
			t.Errorf("GitLab %s needs = %v, want optional", id, needs)
		}
	}
	if err := validate.File("gitlab", ".gitlab-ci.yml", body); err != nil && !errors.Is(err, validate.ErrNoSchema) {
		t.Errorf("GitLab schema: %v\n%s", err, body)
	}
}

const fanProbe = "resources:\n- {name: m, type: git, source: {uri: u, branch: main}}\n- {name: d, type: git, source: {uri: u, branch: dev}}\njobs:\n" +
	"- name: a\n  plan:\n  - {get: m, trigger: true}\n" + task +
	"- name: b\n  plan:\n  - {get: d, trigger: true}\n" + task +
	"- name: fan\n  plan:\n  - {get: m, passed: [a, b], trigger: true}\n" + task

func TestFanInOfDifferentConditionsIsLostAndManual(t *testing.T) {
	pl, rep := parseYAML(t, fanProbe)
	fan := job(t, pl, "fan")
	if !fan.Manual || fan.If != nil {
		t.Errorf("fan must be manual with no condition: manual=%v if=%q", fan.Manual, cond(fan.If))
	}
	found := false
	for _, e := range rep.Entries {
		if strings.Contains(e.IRPath, "job/fan") && e.Feature == caps.JobIf && e.Level == caps.Lost {
			found = true
		}
	}
	if !found {
		t.Errorf("no lost job.if entry for fan: %v", rep.Entries)
	}
	same, _ := parseYAML(t, strings.Replace(fanProbe, "- name: b\n  plan:\n  - {get: d, trigger: true}", "- name: b\n  plan:\n  - {get: m, trigger: true}\n"+task+"- name: c\n  plan:\n  - {get: d, trigger: true}", 1))
	if f := job(t, same, "fan"); f.Manual || f.If == nil {
		t.Errorf("identical conditions must still be inherited: manual=%v if=%q", f.Manual, cond(f.If))
	}
}

func TestBranchCreationIsTheCreatedFieldOnGHA(t *testing.T) {
	zero := expr.Lit{Kind: expr.LitStr, Str: "0000000000000000000000000000000000000000"}
	ref := expr.Ref{Path: []string{expr.CtxBeforeSHA}}
	for _, c := range []struct {
		in   expr.Expr
		want string
		ok   bool
	}{
		{expr.Binary{Op: expr.OpEq, L: ref, R: zero}, "created", true},
		{expr.Binary{Op: expr.OpNe, L: ref, R: zero}, "!created", true},
		{expr.Binary{Op: expr.OpEq, L: ref, R: expr.Lit{Kind: expr.LitStr, Str: "abc"}}, "", false},
	} {
		got, _, ok := expr.TranslateApprox(c.in, exprconcourse.Lang, exprgha.Lang)
		if ok != c.ok || (ok && expr.String(got) != c.want) {
			t.Errorf("%s: got %v, %v want %q, %v", expr.String(c.in), got, ok, c.want, c.ok)
		}
	}
}
