package drone_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func whenOf(t *testing.T, when string) (*ir.Step, *ir.Job) {
	t.Helper()
	j, _ := parseJob(t, stepHead+"- name: s\n  image: x\n  when:\n"+when)
	return step(t, j, "s"), j
}

func TestStepWhenBecomesIf(t *testing.T) {
	s, _ := whenOf(t, "    branch: [main]\n    event: [pull_request]\n")
	if got := condText(s.If); got != "(event == 'pull_request') && (branch == 'main')" {
		t.Fatalf("if = %q", got)
	}
	if s.Ext != nil {
		t.Fatalf("ext = %v", s.Ext)
	}
}

func TestEventPushAloneIsPushOnBranch(t *testing.T) {
	s, _ := whenOf(t, "    event: push\n")
	if got := condText(s.If); got != "(event == 'push') && (ref_type == 'branch')" {
		t.Fatalf("if = %q", got)
	}
}

func TestStepWhenStrictFormsAreOpaqueWithEntry(t *testing.T) {
	for name, when := range map[string]string{
		"scalar include": "    branch:\n      include: main\n",
		"null":           "    branch:\n",
		"nested map":     "    branch:\n      x:\n        y: 1\n",
		"repeated key":   "    branch: a\n    branch: b\n",
	} {
		t.Run(name, func(t *testing.T) {
			j, rep := parseJob(t, stepHead+"- name: s\n  image: x\n  when:\n"+when)
			s := step(t, j, "s")
			if _, ok := s.If.(expr.Opaque); !ok {
				t.Fatalf("if = %#v, want opaque", s.If)
			}
			if !hasEntry(rep, "kept as opaque Drone text") {
				t.Fatal("no report entry")
			}
		})
	}
}

func TestStepWhenUnmodelledKeysStayInExt(t *testing.T) {
	s, _ := whenOf(t, "    branch: main\n    action: opened\n    instance: ci.example.com\n")
	if condText(s.If) != "branch == 'main'" {
		t.Fatalf("if = %q", condText(s.If))
	}
	w, _ := ext(s.Ext)["when"].(map[string]any)
	if !reflect.DeepEqual(w, map[string]any{"action": "opened", "instance": "ci.example.com"}) {
		t.Fatalf("when ext = %v", s.Ext)
	}
}

func TestStepWhenStatus(t *testing.T) {
	s, _ := whenOf(t, "    status: [success, failure]\n")
	if condText(s.If) != "always()" {
		t.Fatalf("if = %q", condText(s.If))
	}
}

func TestDollarEscapeInWhenValue(t *testing.T) {
	s, _ := whenOf(t, "    branch: 'a$$b'\n")
	if condText(s.If) != "branch == 'a$b'" {
		t.Fatalf("if = %q", condText(s.If))
	}
}

func trigYML(trigger string) string {
	return "kind: pipeline\nname: p\ntrigger:\n" + trigger + "steps:\n- {name: s, image: x}\n"
}

func TestPureTriggerFilters(t *testing.T) {
	pl, _ := parseYML(t, trigYML("  event: [push, pull_request, tag, cron, custom]\n"))
	// Normalize sorts triggers by kind.
	want := []ir.Trigger{{Kind: ir.TriggerManual}, {Kind: ir.TriggerPR}, {Kind: ir.TriggerPush}, {Kind: ir.TriggerSchedule}, {Kind: ir.TriggerTag}}
	if !reflect.DeepEqual(pl.Triggers, want) {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	if pl.Jobs[0].If != nil {
		t.Fatalf("a pure trigger must not become Job.If: %v", pl.Jobs[0].If)
	}
}

func TestTriggerBranchFilter(t *testing.T) {
	pl, _ := parseYML(t, trigYML("  event: [push, pull_request]\n  branch:\n    include: [main, 'release/*']\n    exclude: [wip]\n"))
	f := ir.Filter{Include: []string{"main", "release/*"}, Exclude: []string{"wip"}}
	want := []ir.Trigger{{Kind: ir.TriggerPR, Branches: f}, {Kind: ir.TriggerPush, Branches: f}}
	if !reflect.DeepEqual(pl.Triggers, want) {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
}

func TestTriggerPathsKeepsFilterWithEntry(t *testing.T) {
	pl, rep := parseYML(t, trigYML("  event: [push]\n  paths: ['docs/**']\n"))
	if len(pl.Triggers) != 1 || !reflect.DeepEqual(pl.Triggers[0].Paths, ir.Filter{Include: []string{"docs/**"}}) {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
	if !hasEntry(rep, "does not evaluate it") {
		t.Fatal("paths must have a report entry")
	}
}

func TestNotPureTriggerGoesToJobIf(t *testing.T) {
	for name, trig := range map[string]string{
		"ref":             "  ref: ['refs/tags/v*']\n",
		"repo":            "  repo: [acme/app]\n",
		"status":          "  status: [failure]\n",
		"branch only":     "  branch: [main]\n",
		"branch with tag": "  event: [tag]\n  branch: [main]\n",
		"event exclude":   "  event:\n    exclude: [tag]\n",
		"promote":         "  event: [promote]\n",
		"glob event":      "  event: ['p*']\n",
		"unknown key":     "  event: [push]\n  action: [opened]\n",
	} {
		t.Run(name, func(t *testing.T) {
			pl, _ := parseYML(t, trigYML(trig))
			if len(pl.Triggers) != 0 {
				t.Fatalf("triggers = %+v, want none", pl.Triggers)
			}
			if pl.Jobs[0].If == nil && name != "unknown key" {
				t.Fatal("the condition must not vanish")
			}
		})
	}
}

func TestTriggerExtraKeysStayInJobExt(t *testing.T) {
	pl, rep := parseYML(t, trigYML("  event: [pull_request]\n  action: [opened]\n"))
	j := pl.Jobs[0]
	tr, _ := ext(j.Ext)["trigger"].(map[string]any)
	if !reflect.DeepEqual(tr, map[string]any{"action": []any{"opened"}}) || !hasEntry(rep, "trigger.action has no IR field") {
		t.Fatalf("trigger ext = %v", j.Ext)
	}
	if condText(j.If) != "event == 'pull_request'" {
		t.Fatalf("if = %q", condText(j.If))
	}
}

func TestTriggerPushAloneWithRefGoesToIf(t *testing.T) {
	pl, _ := parseYML(t, trigYML("  event: push\n  ref: refs/heads/main\n"))
	if got := condText(pl.Jobs[0].If); got != "(event == 'push') && (ref_type == 'branch') && (ref == 'refs/heads/main')" || len(pl.Triggers) != 0 {
		t.Fatalf("if = %q triggers=%v", got, pl.Triggers)
	}
}

func TestStrictTriggerIsOpaqueNotUnguarded(t *testing.T) {
	pl, rep := parseYML(t, trigYML("  branch:\n    include: main\n"))
	if _, ok := pl.Jobs[0].If.(expr.Opaque); !ok || !hasEntry(rep, "kept as opaque Drone text") {
		t.Fatalf("if = %#v", pl.Jobs[0].If)
	}
}

func TestTriggersShareOnlyWhenEveryPipelineAgrees(t *testing.T) {
	a := "kind: pipeline\nname: a\ntrigger:\n  event: [push]\n  branch: [main]\nsteps:\n- {name: s, image: x}\n"
	b := "---\nkind: pipeline\nname: b\ntrigger:\n  event: [push]\n  branch: [main]\nsteps:\n- {name: s, image: x}\n"
	pl, _ := parseYML(t, a+b)
	if len(pl.Triggers) != 1 || pl.Jobs[0].If != nil || pl.Jobs[1].If != nil {
		t.Fatalf("shared triggers: %+v ifs=%v %v", pl.Triggers, pl.Jobs[0].If, pl.Jobs[1].If)
	}
	c := "---\nkind: pipeline\nname: c\nsteps:\n- {name: s, image: x}\n"
	pl, _ = parseYML(t, a+c)
	if len(pl.Triggers) != 0 || pl.Jobs[0].If == nil {
		t.Fatalf("a pipeline without a trigger forces the others onto Job.If: %+v", pl.Triggers)
	}
}

func TestTriggerPushIsBranchPushesOnly(t *testing.T) {
	pl, rep := parseYML(t, trigYML("  event: [push]\n"))
	if want := []ir.Trigger{{Kind: ir.TriggerPush}}; !reflect.DeepEqual(pl.Triggers, want) {
		t.Fatalf("triggers = %+v, want a push trigger and no tag trigger", pl.Triggers)
	}
	if pl.Jobs[0].If != nil || len(rep.Entries) != 0 {
		t.Fatalf("if = %v, entries = %+v", pl.Jobs[0].If, rep.Entries)
	}
	pl, _ = parseYML(t, trigYML("  event: [push, tag]\n"))
	if want := []ir.Trigger{{Kind: ir.TriggerPush}, {Kind: ir.TriggerTag}}; !reflect.DeepEqual(pl.Triggers, want) {
		t.Fatalf("triggers = %+v", pl.Triggers)
	}
}

func TestTriggersThatDifferGoToJobIf(t *testing.T) {
	doc := func(name, event string) string {
		return "kind: pipeline\nname: " + name + "\ntrigger:\n  event: [" + event + "]\nsteps:\n- {name: s, image: x}\n"
	}
	pl, _ := parseYML(t, doc("a", "push")+"---\n"+doc("b", "pull_request"))
	if len(pl.Triggers) != 0 {
		t.Fatalf("triggers = %+v, want none: the documents disagree", pl.Triggers)
	}
	if condText(pl.Jobs[1].If) != "event == 'pull_request'" {
		t.Fatalf("job b if = %q", condText(pl.Jobs[1].If))
	}
	if got := condText(pl.Jobs[0].If); got != "(event == 'push') && (ref_type == 'branch')" {
		t.Fatalf("job a if = %q, want the push-only condition", got)
	}
}

func TestTagTriggerReadsRefFilter(t *testing.T) {
	pl, _ := parseYML(t, trigYML("  event: [tag]\n  ref:\n    include: ['refs/tags/v*']\n    exclude: ['refs/tags/v0*']\n"))
	want := []ir.Trigger{{Kind: ir.TriggerTag, Tags: ir.Filter{Include: []string{"v*"}, Exclude: []string{"v0*"}}}}
	if !reflect.DeepEqual(pl.Triggers, want) || pl.Jobs[0].If != nil {
		t.Fatalf("triggers = %+v if = %v", pl.Triggers, pl.Jobs[0].If)
	}
}

func TestRefFilterThatIsNotATagFilterStaysACondition(t *testing.T) {
	for name, trig := range map[string]string{
		"branch ref":    "  event: [tag]\n  ref: ['refs/heads/main']\n",
		"push and tag":  "  event: [push, tag]\n  ref: ['refs/tags/v*']\n",
		"bare tags ref": "  event: [tag]\n  ref: ['refs/tags/']\n",
		"with branch":   "  event: [tag]\n  branch: [main]\n  ref: ['refs/tags/v*']\n",
	} {
		t.Run(name, func(t *testing.T) {
			pl, _ := parseYML(t, trigYML(trig))
			if len(pl.Triggers) != 0 || pl.Jobs[0].If == nil {
				t.Fatalf("triggers = %+v if = %v", pl.Triggers, pl.Jobs[0].If)
			}
		})
	}
}

func TestCronNameIsTheScheduleTrigger(t *testing.T) {
	pl, rep := parseYML(t, trigYML("  event: [cron]\n  cron: [nightly]\n"))
	if len(pl.Triggers) != 1 || pl.Triggers[0].Kind != ir.TriggerSchedule || pl.Jobs[0].If != nil {
		t.Fatalf("triggers = %+v if %v", pl.Triggers, pl.Jobs[0].If)
	}
	if !hasEntry(rep, "trigger.cron names cron jobs") {
		t.Errorf("the cron name must be reported: %+v", rep.Entries)
	}
}
