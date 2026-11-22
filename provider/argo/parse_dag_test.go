package argo_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/ir"
)

func TestDagTasksBecomeJobsWithNeeds(t *testing.T) {
	pl, _ := parseYAML(t, dagOf(`      - {name: build, template: echo}
      - {name: test, template: echo, dependencies: [build]}
      - {name: lint, template: echo, continueOn: {failed: true}}
      - {name: ship, template: echo, dependencies: [build, test], when: "{{workflow.parameters.ctx-branch}} == main"}
`, echoTemplates))
	if !reflect.DeepEqual(ids(pl.Jobs), []string{"build", "lint", "ship", "test"}) {
		t.Fatalf("jobs %v", ids(pl.Jobs))
	}
	if got := needIDs(job(t, pl, "ship")); !reflect.DeepEqual(got, []string{"build", "test"}) {
		t.Fatalf("ship needs %v", got)
	}
	if !job(t, pl, "lint").AllowFailure || job(t, pl, "build").AllowFailure {
		t.Fatal("continueOn.failed not mapped to AllowFailure")
	}
	if got := cond(job(t, pl, "ship").If); got != "branch == 'main'" {
		t.Fatalf("ship If = %q", got)
	}
	if ext(job(t, pl, "lint").Ext)["continueOn"] != nil {
		t.Fatalf("consumed continueOn kept: %v", ext(job(t, pl, "lint").Ext))
	}
}

func TestContinueOnErrorIsKept(t *testing.T) {
	pl, rep := parseYAML(t, dagOf("      - {name: a, template: echo, continueOn: {failed: true, error: true}}\n", echoTemplates))
	a := job(t, pl, "a")
	if !a.AllowFailure || ext(a.Ext)["continueOn"] == nil || !hasEntry(rep, "continueOn") {
		t.Fatalf("a = %+v %v", a, ext(a.Ext))
	}
}

func TestOpaqueWhenIsReported(t *testing.T) {
	pl, rep := parseYAML(t, dagOf("      - {name: a, template: echo, when: \"{{workflow.name}} =~ ^rel\"}\n", echoTemplates))
	if job(t, pl, "a").If == nil || !hasEntry(rep, "opaque Argo text") {
		t.Fatalf("If = %v, entries %+v", job(t, pl, "a").If, rep.Entries)
	}
}

func TestDependsPlainSuccessIsAPlainNeed(t *testing.T) {
	pl, _ := parseYAML(t, dagOf(`      - {name: a, template: echo}
      - {name: b, template: echo}
      - {name: c, template: echo, depends: "a.Succeeded && b.Succeeded"}
`, echoTemplates))
	c := job(t, pl, "c")
	if !reflect.DeepEqual(needIDs(c), []string{"a", "b"}) || c.If != nil {
		t.Fatalf("c needs %v If %v", needIDs(c), c.If)
	}
}

// A bare depends atom also passes on Skipped and Daemoned, so it is no plain success need.
// res is the condition needs.<job>.result == <status>.
func res(job, status string) expr.Expr {
	return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.PrefixNeeds, job, "result"}}, R: expr.Lit{Kind: expr.LitStr, Str: status}}
}

func and(l, r expr.Expr) expr.Expr { return expr.Binary{Op: expr.OpAnd, L: l, R: r} }
func or(l, r expr.Expr) expr.Expr  { return expr.Binary{Op: expr.OpOr, L: l, R: r} }
func not(x expr.Expr) expr.Expr    { return expr.Unary{Op: expr.OpNot, X: x} }

// A bare depends atom also passes on Skipped and Daemoned, so it is no plain success need.
func TestDependsBareAtomIsNotPlainSuccess(t *testing.T) {
	pl, rep := parseYAML(t, dagOf(`      - {name: a, template: echo}
      - {name: c, template: echo, depends: a}
`, echoTemplates))
	c := job(t, pl, "c")
	if !reflect.DeepEqual(needIDs(c), []string{"a"}) {
		t.Fatalf("needs %v", needIDs(c))
	}
	if got, want := cond(c.If), expr.String(or(res("a", "success"), res("a", "skipped"))); got != want {
		t.Fatalf("If = %q, want %q", got, want)
	}
	if ext(c.Ext)["depends"] != "a" || !hasEntry(rep, "Daemoned") {
		t.Fatalf("depends not kept: %v %+v", ext(c.Ext), rep.Entries)
	}
}

func TestDependsStatusAtoms(t *testing.T) {
	cases := map[string]expr.Expr{
		"a.Failed":  res("a", "failure"),
		"a.Skipped": res("a", "skipped"),
		"!a.Failed": not(res("a", "failure")),
		"(a.Succeeded || a.Skipped) && !b.Failed": and(or(res("a", "success"), res("a", "skipped")), not(res("b", "failure"))),
		"a.Failed || b.Succeeded":                 or(res("a", "failure"), res("b", "success")),
		"a.Succeeded && b.Failed":                 and(res("a", "success"), res("b", "failure")),
	}
	for depends, want := range cases {
		t.Run(depends, func(t *testing.T) {
			pl, _ := parseYAML(t, dagOf("      - {name: a, template: echo}\n      - {name: b, template: echo}\n      - {name: c, template: echo, depends: \""+depends+"\"}\n", echoTemplates))
			c := job(t, pl, "c")
			if got := cond(c.If); got != expr.String(want) {
				t.Fatalf("If = %q, want %q", got, expr.String(want))
			}
			if len(c.Needs) == 0 {
				t.Fatal("no needs")
			}
		})
	}
}

// An atom the IR cannot hold never widens or inverts the condition: it reads as false where it is positive.
func TestDependsAtomsWithNoIRStatusFailSafe(t *testing.T) {
	off := expr.Lit{Kind: expr.LitBool, Bool: false}
	cases := map[string]expr.Expr{
		"a.Daemoned":                off,
		"a.Errored":                 off,
		"a.Omitted":                 off,
		"a.AnySucceeded":            off,
		"a.AllFailed":               off,
		"!a.Errored":                off,
		"a.Succeeded && a.Errored":  off,
		"a.Failed || a.Errored":     res("a", "failure"),
		"a.Succeeded || a.Daemoned": res("a", "success"),
		"a.Skipped || a.Omitted":    res("a", "skipped"),
		"!(a.Failed || a.Errored)":  off,
	}
	for depends, want := range cases {
		t.Run(depends, func(t *testing.T) {
			pl, rep := parseYAML(t, dagOf("      - {name: a, template: echo}\n      - {name: c, template: echo, depends: \""+depends+"\"}\n", echoTemplates))
			c := job(t, pl, "c")
			if !reflect.DeepEqual(needIDs(c), []string{"a"}) || ext(c.Ext)["depends"] != depends {
				t.Fatalf("needs %v ext %v", needIDs(c), ext(c.Ext))
			}
			if c.If == nil || expr.String(c.If) != expr.String(want) {
				t.Fatalf("If = %v, want %v", c.If, expr.String(want))
			}
			if !hasEntry(rep, depends) {
				t.Fatalf("no entry: %+v", rep.Entries)
			}
		})
	}
}

func TestLoopsAreKeptInExt(t *testing.T) {
	pl, rep := parseYAML(t, dagOf(`      - {name: a, template: echo, withItems: [1, 2]}
      - {name: b, template: echo, withItems: [{os: linux}, {os: mac}]}
      - {name: c, template: echo, withParam: "{{tasks.a.outputs.result}}"}
      - {name: d, template: echo, withSequence: {count: "3"}}
`, echoTemplates))
	for id, key := range map[string]string{"a": "withItems", "b": "withItems", "c": "withParam", "d": "withSequence"} {
		j := job(t, pl, id)
		if ext(j.Ext)[key] == nil || j.Matrix != nil {
			t.Fatalf("%s: Ext %v Matrix %v", id, ext(j.Ext), j.Matrix)
		}
		if !hasEntry(rep, "task "+id+": "+key) {
			t.Fatalf("no entry for %s %s: %+v", id, key, rep.Entries)
		}
	}
}

func TestStepsAreSequentialGroupsOfParallelSteps(t *testing.T) {
	pl, _ := parseYAML(t, wt(`  - name: main
    steps:
    - - {name: checkout, template: echo}
    - - {name: build, template: echo}
      - {name: lint, template: echo}
    - - {name: ship, template: echo, when: "{{workflow.parameters.ctx-branch}} == main"}
`+echoTemplates))
	if !reflect.DeepEqual(ids(pl.Jobs), []string{"build", "checkout", "lint", "ship"}) {
		t.Fatalf("jobs %v", ids(pl.Jobs))
	}
	if got := needIDs(job(t, pl, "build")); !reflect.DeepEqual(got, []string{"checkout"}) {
		t.Fatalf("build needs %v", got)
	}
	if got := needIDs(job(t, pl, "ship")); !reflect.DeepEqual(got, []string{"build", "lint"}) {
		t.Fatalf("ship needs %v", got)
	}
	if len(job(t, pl, "checkout").Needs) != 0 {
		t.Fatal("first group has needs")
	}
}

func TestEntrypointThatIsAJobTemplateIsOneJob(t *testing.T) {
	pl, _ := parseYAML(t, wt("  - {name: main, container: {image: x, command: [make]}}\n"))
	if !reflect.DeepEqual(ids(pl.Jobs), []string{"main"}) {
		t.Fatalf("jobs %v", ids(pl.Jobs))
	}
}

func TestSuspendTemplateIsAManualGate(t *testing.T) {
	pl, rep := parseYAML(t, dagOf(`      - {name: build, template: echo}
      - {name: approve, template: gate, dependencies: [build]}
      - {name: deploy, template: echo, dependencies: [approve]}
`, echoTemplates+"  - {name: gate, suspend: {}}\n"))
	if !reflect.DeepEqual(ids(pl.Jobs), []string{"build", "deploy"}) {
		t.Fatalf("jobs %v", ids(pl.Jobs))
	}
	d := job(t, pl, "deploy")
	if !d.Manual || !reflect.DeepEqual(needIDs(d), []string{"build"}) {
		t.Fatalf("deploy manual=%v needs=%v", d.Manual, needIDs(d))
	}
	gates, _ := ext(d.Ext)["gates"].([]any)
	if len(gates) != 1 || !strings.Contains(fmtAny(gates[0]), "approve") || !hasEntry(rep, "suspend") {
		t.Fatalf("gate not kept: %v %+v", gates, rep.Entries)
	}
}

func TestSuspendWithDurationOrOutputsStaysAJob(t *testing.T) {
	pl, rep := parseYAML(t, dagOf(`      - {name: wait, template: delay}
      - {name: after, template: echo, dependencies: [wait]}
      - {name: tail-gate, template: gate, dependencies: [after]}
`, echoTemplates+"  - {name: delay, suspend: {duration: 20s}}\n  - {name: gate, suspend: {}}\n"))
	wait := job(t, pl, "wait")
	if len(wait.Steps) != 1 || wait.Steps[0].Kind != ir.StepTask || ext(wait.Steps[0].Ext)["template"] == nil {
		t.Fatalf("delay not kept as a task step: %+v", wait.Steps)
	}
	if job(t, pl, "after").Manual {
		t.Fatal("a delay became a manual gate")
	}
	// A gate nothing depends on has no job to mark, so it stays a job.
	if job(t, pl, "tail-gate") == nil || !hasEntry(rep, "suspend") {
		t.Fatalf("entries %+v", rep.Entries)
	}
}

func TestGateReferencedByAConditionStaysAJob(t *testing.T) {
	pl, _ := parseYAML(t, dagOf(`      - {name: approve, template: gate}
      - {name: deploy, template: echo, depends: "approve.Succeeded"}
      - {name: x, template: echo, depends: "approve.Failed"}
`, echoTemplates+"  - {name: gate, suspend: {}}\n"))
	if job(t, pl, "approve") == nil {
		t.Fatal("gate removed although a status refers to it")
	}
}

// A negated bare atom would be wider than Argo, which also passes a bare atom on Daemoned.
func TestNegatedBareAtomFailsSafe(t *testing.T) {
	off := expr.Lit{Kind: expr.LitBool, Bool: false}
	for _, depends := range []string{"!a", "!(a)", "!a && b", "!(a || b)", "!!!a"} {
		t.Run(depends, func(t *testing.T) {
			pl, rep := parseYAML(t, dagOf("      - {name: a, template: echo}\n      - {name: b, template: echo}\n      - {name: c, template: echo, depends: \""+depends+"\"}\n", echoTemplates))
			c := job(t, pl, "c")
			if c.If == nil || expr.String(c.If) != expr.String(off) || ext(c.Ext)["depends"] != depends || !hasEntry(rep, depends) {
				t.Fatalf("If = %v ext %v", c.If, ext(c.Ext))
			}
		})
	}
	pl, _ := parseYAML(t, dagOf("      - {name: a, template: echo}\n      - {name: c, template: echo, depends: \"!!a\"}\n", echoTemplates))
	if got, want := cond(job(t, pl, "c").If), expr.String(not(not(or(res("a", "success"), res("a", "skipped"))))); got != want {
		t.Fatalf("!!a If = %q, want %q", got, want)
	}
}
