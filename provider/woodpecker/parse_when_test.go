package woodpecker_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/ir"
)

const body = "steps:\n  - name: s\n    image: x\n    commands: [y]\n"

func TestPureWhenBecomesTriggers(t *testing.T) {
	pl, j, _ := single(t, "when:\n  - event: push\n    branch: main\n    path: [\"src/**\"]\n  - event: tag\n  - event: pull_request\n"+body)
	want := []ir.Trigger{
		{Kind: ir.TriggerPR},
		{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}, Paths: ir.Filter{Include: []string{"src/**"}}},
		{Kind: ir.TriggerTag},
	}
	if !reflect.DeepEqual(pl.Triggers, want) {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	if j.If != nil {
		t.Fatalf("a pure trigger filter must not also be a job condition: %s", text(j.If))
	}
}

// Review Focus 2: list items are OR, keys inside one item are AND.
func TestWhenListIsOrNotAnd(t *testing.T) {
	pl, _, _ := single(t, "when:\n  - event: push\n    branch: main\n  - event: tag\n"+body)
	if len(pl.Triggers) != 2 || pl.Triggers[0].Kind != ir.TriggerPush || pl.Triggers[1].Kind != ir.TriggerTag {
		t.Fatalf("OR list collapsed: %+v", pl.Triggers)
	}
	if !reflect.DeepEqual(pl.Triggers[0].Branches.Include, []string{"main"}) || len(pl.Triggers[1].Branches.Include) != 0 {
		t.Fatalf("branch filter leaked across items: %+v", pl.Triggers)
	}
}

func TestWhenListOrInCondition(t *testing.T) {
	pl, j, _ := single(t, "when:\n  - event: push\n    branch: main\n  - event: tag\n    evaluate: 'CI_COMMIT_TAG != \"\"'\n"+body)
	if len(pl.Triggers) != 0 {
		t.Fatalf("an evaluate makes the filter impure: %+v", pl.Triggers)
	}
	got := text(j.If)
	want := `((event == 'push') && ((event == 'tag') || (branch == 'main'))) || ((event == 'tag') && (tag != ''))`
	if got != want {
		t.Fatalf("If = %s\nwant %s", got, want)
	}
}

func TestEventListInOneItemIsOneOr(t *testing.T) {
	pl, _, _ := single(t, "when:\n  event: [push, tag]\n  branch: main\n"+body)
	want := []ir.Trigger{
		{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"main"}}},
		{Kind: ir.TriggerTag},
	}
	if !reflect.DeepEqual(pl.Triggers, want) {
		t.Fatalf("branch must not narrow the tag trigger: %+v", pl.Triggers)
	}
}

func TestGlobBranchInTrigger(t *testing.T) {
	pl, j, _ := single(t, "when:\n  event: push\n  branch:\n    include: [\"release/*\", main]\n    exclude: [\"wip-*\"]\n"+body)
	want := []ir.Trigger{{Kind: ir.TriggerPush, Branches: ir.Filter{Include: []string{"release/*", "main"}, Exclude: []string{"wip-*"}}}}
	if !reflect.DeepEqual(pl.Triggers, want) || j.If != nil {
		t.Fatalf("triggers = %+v if = %s", pl.Triggers, text(j.If))
	}
}

func TestInexactGlobStaysAConditionNotATrigger(t *testing.T) {
	for _, pat := range []string{"release/**", "feat/{a,b}", "v?"} {
		pl, j, rep := single(t, "when:\n  event: push\n  branch: '"+pat+"'\n"+body)
		if len(pl.Triggers) != 0 {
			t.Errorf("%s: glob with no exact IR filter form became a trigger: %+v", pat, pl.Triggers)
		}
		if got := text(j.If); !strings.Contains(got, "glob") || !strings.Contains(got, pat) {
			t.Errorf("%s: condition = %q", pat, got)
		}
		if !hasEntry(rep, pat) {
			t.Errorf("%s: no report entry for the opaque glob: %+v", pat, rep.Entries)
		}
	}
}

// IR consumers read a leading ! as negation and + as a quantifier; doublestar reads both as literal.
// The value stays an exact equality in the condition and never reaches a trigger filter.
func TestIRMetacharactersStayLiteralConditions(t *testing.T) {
	for _, pat := range []string{"!main", "feat+", "a+b"} {
		pl, j, rep := single(t, "when:\n  event: push\n  branch: '"+pat+"'\n"+body)
		if len(pl.Triggers) != 0 {
			t.Errorf("%s: became a trigger filter: %+v", pat, pl.Triggers)
		}
		if got := text(j.If); !strings.Contains(got, "branch == '"+pat+"'") || strings.Contains(got, "glob") {
			t.Errorf("%s: condition = %q", pat, got)
		}
		if len(rep.Entries) != 0 {
			t.Errorf("%s: an exact equality needs no entry: %+v", pat, rep.Entries)
		}
	}
	pl, _, _ := single(t, "when:\n  event: push\n  path: ['!docs/*']\n"+body)
	if len(pl.Triggers) != 0 {
		t.Errorf("a path with a leading ! became a trigger filter: %+v", pl.Triggers)
	}
}

func TestWhenWithOnlyUnmodelledKeysIsNotNoRestriction(t *testing.T) {
	_, j, rep := single(t, "when:\n  platform: linux/*\n"+body)
	w, ok := ext(j.Ext)["when"]
	if !ok {
		t.Fatalf("the platform filter is not kept: %v", j.Ext)
	}
	if !reflect.DeepEqual(w, map[string]any{"platform": "linux/*"}) {
		t.Fatalf("when = %#v", w)
	}
	if !hasEntry(rep, "platform") {
		t.Fatalf("no report entry: %+v", rep.Entries)
	}
}

func TestWhenCronNameAndMatrixAreKept(t *testing.T) {
	_, j, rep := single(t, "when:\n  event: cron\n  cron: nightly\n"+body)
	if _, ok := ext(j.Ext)["when"]; !ok || !hasEntry(rep, "cron") {
		t.Fatalf("cron name filter not kept: %v %+v", j.Ext, rep.Entries)
	}
}

func TestWorkflowStatus(t *testing.T) {
	_, j, _ := single(t, "when:\n  - status: [success, failure]\n"+body)
	if text(j.If) != "always()" {
		t.Fatalf("If = %q", text(j.If))
	}
	_, j, _ = single(t, "when:\n  - event: push\n    status: failure\n"+body)
	if text(j.If) != `(event == 'push') && failure()` {
		t.Fatalf("If = %q", text(j.If))
	}
}

func TestWorkflowsWithDifferentWhenUseJobConditions(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		".woodpecker/a.yaml": "when:\n  event: push\n" + body,
		".woodpecker/b.yaml": "when:\n  event: tag\n" + body,
	})
	pl := p.Pipelines[0]
	if len(pl.Triggers) != 0 {
		t.Fatalf("workflows with different filters cannot share pipeline triggers: %+v", pl.Triggers)
	}
	got := map[string]string{}
	for _, j := range pl.Jobs {
		got[j.ID] = text(j.If)
	}
	if got["a"] != "event == 'push'" || got["b"] != "event == 'tag'" {
		t.Fatalf("conditions = %v", got)
	}
}

func TestWorkflowsWithSameWhenShareTriggers(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		".woodpecker/a.yaml": "when:\n  event: push\n" + body,
		".woodpecker/b.yaml": "when:\n  event: push\n" + body,
	})
	pl := p.Pipelines[0]
	if !reflect.DeepEqual(pl.Triggers, []ir.Trigger{{Kind: ir.TriggerPush}}) || pl.Jobs[0].If != nil || pl.Jobs[1].If != nil {
		t.Fatalf("triggers %+v jobs %+v %+v", pl.Triggers, pl.Jobs[0].If, pl.Jobs[1].If)
	}
}

func TestOneWorkflowWithoutWhenKeepsAllConditions(t *testing.T) {
	p, _ := parseFiles(t, map[string]string{
		".woodpecker/a.yaml": "when:\n  event: push\n" + body,
		".woodpecker/b.yaml": body,
	})
	pl := p.Pipelines[0]
	if len(pl.Triggers) != 0 || pl.Jobs[0].If == nil || pl.Jobs[1].If != nil {
		t.Fatalf("triggers %+v ifs %v %v", pl.Triggers, pl.Jobs[0].If, pl.Jobs[1].If)
	}
}

func TestStepWhen(t *testing.T) {
	_, j, rep := single(t, `steps:
  - name: a
    image: x
    commands: [y]
    when:
      - event: push
        branch: main
      - event: tag
  - name: b
    image: x
    commands: [y]
    when:
      status: [failure]
  - name: c
    image: x
    commands: [y]
    when:
      event: push
      path: ["docs/**"]
`)
	if got := text(step(t, j, "a").If); got != `((event == 'push') && ((event == 'tag') || (branch == 'main'))) || (event == 'tag')` {
		t.Fatalf("a.If = %s", got)
	}
	if got := text(step(t, j, "b").If); got != "failure()" {
		t.Fatalf("b.If = %s", got)
	}
	c := step(t, j, "c")
	if text(c.If) != "event == 'push'" {
		t.Fatalf("c.If = %s", text(c.If))
	}
	if _, ok := ext(c.Ext)["when"]; !ok || !hasEntry(rep, "path") {
		t.Fatalf("a step path filter must be kept and reported: %v %+v", c.Ext, rep.Entries)
	}
}

func TestStepWhenMixedStatusIsKept(t *testing.T) {
	_, j, rep := single(t, "steps:\n  - name: a\n    image: x\n    commands: [y]\n    when:\n      - event: push\n        status: failure\n      - event: tag\n")
	a := step(t, j, "a")
	if text(a.If) != "(event == 'push') || (event == 'tag')" {
		t.Fatalf("If = %s", text(a.If))
	}
	if _, ok := ext(a.Ext)["when"]; !ok || !hasEntry(rep, "status") {
		t.Fatalf("status that differs per item must be kept: %v %+v", a.Ext, rep.Entries)
	}
}

func TestEvaluateUnescapesDollar(t *testing.T) {
	_, j, _ := single(t, "steps:\n  - name: a\n    image: x\n    commands: [y]\n    when:\n      evaluate: 'CI_COMMIT_MESSAGE == \"$$x\"'\n")
	if got := text(step(t, j, "a").If); got != "woodpecker.CI_COMMIT_MESSAGE == '$x'" {
		t.Fatalf("If = %q", got)
	}
}

func TestBadEvaluateIsAnError(t *testing.T) {
	err := parseErr(t, map[string]string{".woodpecker.yaml": "when:\n  evaluate: 'a =='\n" + body})
	if !strings.Contains(err.Error(), "when") {
		t.Fatalf("error does not name the when: %v", err)
	}
}
