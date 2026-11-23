package woodpecker_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
)

// factsWorkflow is a GHA workflow with the given on: block and a job gated on ref_name.
func factsWorkflow(on, cond string) map[string]string {
	return map[string]string{".github/workflows/ci.yml": "on:\n" + on + "jobs:\n  deploy:\n    runs-on: ubuntu-latest\n    if: " + cond +
		"\n    steps:\n      - run: make\n"}
}

const (
	onPushPR      = "  push:\n    branches: [main]\n  pull_request:\n    branches: [main]\n"
	prExclusion   = `!(CI_PIPELINE_EVENT matches "(?i)^pull_request$$")`
	branchIsMain  = `CI_COMMIT_BRANCH matches "(?i)^main$$"`
	refNameIsMain = "github.ref_name == 'main'"
)

func TestRefNameTranslatesWhenTriggersRuleOutTags(t *testing.T) {
	for name, on := range map[string]string{
		"push and pull request": onPushPR,
	} {
		t.Run(name, func(t *testing.T) {
			p := parseProject(t, "gha", factsWorkflow(on, refNameIsMain))
			text, rep := one(t, p)
			if reportHas(rep, caps.JobIf, caps.Lost) {
				t.Fatalf("job.if lost although no trigger can carry a tag:\n%v\n%s", rep.Entries, text)
			}
			if !strings.Contains(text, prExclusion) || !strings.Contains(text, branchIsMain) {
				t.Errorf("the condition must exclude pull requests and test the branch:\n%s", text)
			}
			back := parseProject(t, "woodpecker", map[string]string{".woodpecker/deploy.yaml": text})
			got := back.Pipelines[0].Jobs[0].If
			if got == nil || expr.String(got) != expr.String(p.Pipelines[0].Jobs[0].If) {
				t.Errorf("the source condition did not return: %v", got)
			}
		})
	}
}

func TestRefNameStaysRefusedWhenATriggerCanCarryATag(t *testing.T) {
	for name, on := range map[string]string{
		"tag trigger":      "  push:\n    branches: [main]\n    tags: ['v*']\n  pull_request:\n",
		"bare push":        "  push:\n",
		"schedule":         "  schedule:\n    - cron: '0 3 * * *'\n",
		"push and cron":    onPushPR + "  schedule:\n    - cron: '0 3 * * *'\n",
		"manual dispatch":  onPushPR + "  workflow_dispatch:\n",
		"repository event": onPushPR + "  repository_dispatch:\n",
		"release":          onPushPR + "  release:\n    types: [published]\n",
		"pr target":        "  pull_request_target:\n    branches: [main]\n",
	} {
		t.Run(name, func(t *testing.T) {
			p := parseProject(t, "gha", factsWorkflow(on, refNameIsMain))
			_, rep := one(t, p)
			if !reportDetail(rep, caps.JobIf, "condition not expressible in Woodpecker CI") {
				t.Errorf("job.if must stay refused:\n%v", rep.Entries)
			}
		})
	}
}

func TestBranchGuardGainsPullRequestExclusion(t *testing.T) {
	p := parseProject(t, "gha", factsWorkflow(onPushPR, "github.ref_type == 'branch' && "+refNameIsMain))
	text, rep := one(t, p)
	if reportHas(rep, caps.JobIf, caps.Lost) {
		t.Fatalf("job.if lost:\n%v", rep.Entries)
	}
	if !strings.Contains(text, prExclusion) {
		t.Errorf("CI_COMMIT_BRANCH is the target branch on a pull request, so the guard must exclude it:\n%s", text)
	}
}

// The plain-run facts are not needed for the pull request test: a guarded comparison gets it on any trigger set.
func TestBranchGuardExcludesPullRequestWithoutFacts(t *testing.T) {
	p := parseProject(t, "gha", factsWorkflow("  push:\n", "github.ref_type == 'branch' && "+refNameIsMain))
	text, rep := one(t, p)
	if reportHas(rep, caps.JobIf, caps.Lost) {
		t.Fatalf("job.if lost:\n%v", rep.Entries)
	}
	if !strings.Contains(text, prExclusion) {
		t.Errorf("the guarded comparison must exclude pull requests:\n%s", text)
	}
}

func TestGuardedNegationAndAffixAreRefused(t *testing.T) {
	for name, cond := range map[string]string{
		"not equal":  "github.ref_type == 'branch' && github.ref_name != 'main'",
		"startsWith": "github.ref_type == 'branch' && startsWith(github.ref_name, 'rel')",
		"endsWith":   "github.ref_type == 'branch' && endsWith(github.ref_name, '-x')",
		"contains":   "github.ref_type == 'branch' && contains(github.ref_name, 'rel')",
	} {
		t.Run(name, func(t *testing.T) {
			p := parseProject(t, "gha", factsWorkflow(onPushPR, cond))
			_, rep := one(t, p)
			if !reportDetail(rep, caps.JobIf, "condition not expressible in Woodpecker CI") {
				t.Errorf("job.if must be refused:\n%v", rep.Entries)
			}
		})
	}
}

func TestNotEqualRefNameGainsPullRequestTest(t *testing.T) {
	p := parseProject(t, "gha", factsWorkflow(onPushPR, "github.ref_name != 'main'"))
	text, rep := one(t, p)
	if reportHas(rep, caps.JobIf, caps.Lost) {
		t.Fatalf("job.if lost:\n%v", rep.Entries)
	}
	if !strings.Contains(text, `CI_PIPELINE_EVENT matches "(?i)^pull_request$$" || !(`+branchIsMain+`)`) {
		t.Errorf("!= must be true on a pull request:\n%s", text)
	}
}
