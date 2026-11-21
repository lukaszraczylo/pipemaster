package drone_test

import (
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"pgregory.net/rapid"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/drone"
)

var (
	event  = r("event")
	branch = r("branch")
)

func TestFilterExpr(t *testing.T) {
	for _, c := range []struct {
		want expr.Expr
		name string
		key  string
		f    drone.Filter
	}{
		{nil, "empty matches all", "event", drone.Filter{}},
		{eq(event, "pull_request"), "single literal", "event", drone.Filter{Include: []string{"pull_request"}}},
		{bin(expr.OpOr, eq(event, "pull_request"), eq(event, "tag")), "list is OR", "event",
			drone.Filter{Include: []string{"pull_request", "tag"}}},
		{bin(expr.OpAnd, eq(r("repo"), "a/b"), not(eq(r("repo"), "a/c"))), "include and exclude", "repo",
			drone.Filter{Include: []string{"a/b"}, Exclude: []string{"a/c"}}},
		{not(eq(r("repo"), "a/c")), "exclude only", "repo", drone.Filter{Exclude: []string{"a/c"}}},
		{eq(branch, "main"), "branch has no tag-event guard", "branch", drone.Filter{Include: []string{"main"}}},
		{drone.GlobOpaque("branch", "release/*"), "glob stays opaque", "branch",
			drone.Filter{Include: []string{"release/*"}}},
		{eq(r("tag"), "v1"), "ref of a tag is tag equality", "ref", drone.Filter{Include: []string{"refs/tags/v1"}}},
		{eq(r("ref"), "refs/heads/main"), "ref of a branch stays ref", "ref",
			drone.Filter{Include: []string{"refs/heads/main"}}},
		{eq(r("ref"), "refs/tags/"), "empty tag name stays ref", "ref", drone.Filter{Include: []string{"refs/tags/"}}},
		{drone.GlobOpaque("ref", "refs/tags/v*"), "tag glob stays opaque", "ref",
			drone.Filter{Include: []string{"refs/tags/v*"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := drone.FilterExpr(c.key, c.f)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("FilterExpr = %#v, %v; want %#v", got, err, c.want)
			}
		})
	}
	if _, err := drone.FilterExpr("paths", drone.Filter{Include: []string{"x"}}); err == nil {
		t.Error("paths has no expression form")
	}
}

func TestToExprStatus(t *testing.T) {
	for _, c := range []struct {
		want   expr.Expr
		name   string
		status []string
	}{
		{nil, "absent is success", nil},
		{expr.Call{Name: "failure"}, "failure", []string{"failure"}},
		{expr.Call{Name: "always"}, "both is always", []string{"success", "failure"}},
		{expr.Call{Name: "success"}, "success alone", []string{"success"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := drone.ToExpr(drone.Conditions{Status: c.status})
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("ToExpr = %#v, %v; want %#v", got, err, c.want)
			}
		})
	}
	if _, err := drone.ToExpr(drone.Conditions{Status: []string{"skipped"}}); err == nil {
		t.Error("an unknown status was accepted")
	}
}

func TestFromExprIdentity(t *testing.T) {
	for _, c := range []struct {
		name string
		c    drone.Conditions
	}{
		{"empty", drone.Conditions{}},
		{"keys AND", drone.Conditions{
			Event:  drone.Filter{Include: []string{"cron", "tag"}},
			Branch: drone.Filter{Include: []string{"main", "release/*"}, Exclude: []string{"wip"}},
			Ref:    drone.Filter{Include: []string{"refs/tags/v1", "refs/heads/x"}},
			Repo:   drone.Filter{Exclude: []string{"a/*"}},
			Status: []string{"failure"},
		}},
		{"always", drone.Conditions{Status: []string{"success", "failure"}}},
		{"disabled", drone.Disabled()},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, err := drone.ToExpr(c.c)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := drone.FromExpr(e, exact)
			if !ok || !reflect.DeepEqual(got, c.c) {
				t.Errorf("FromExpr(ToExpr(c)) = %#v, %v; want %#v", got, ok, c.c)
			}
		})
	}
}

// Review Focus 1: Drone ANDs the keys of one when, so an OR across keys has no filter form.
func TestNoOrAcrossKeys(t *testing.T) {
	gha := bin(expr.OpOr,
		bin(expr.OpAnd, eq(event, "pull_request"), eq(branch, "main")),
		eq(event, "schedule"))
	if c, ok := drone.FromExpr(gha, exact); ok {
		t.Fatalf("an OR across keys became one when: %#v", c)
	}
	if !drone.Guardable(gha, exact) {
		t.Fatal("the condition must fall back to a shell guard")
	}
	line, err := drone.PrintGuard(gha, exact)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `'cron'`) {
		t.Errorf("guard %q must compare the Drone event value", line)
	}
	want := bin(expr.OpOr,
		bin(expr.OpAnd, eq(event, "pull_request"), eq(branch, "main")),
		eq(event, "schedule"))
	if got, ok := drone.ParseGuard(line); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("guard %q parsed to %#v, %v", line, got, ok)
	}
	// The GHA push event has no Drone literal, so the plan's own example cannot be guarded.
	push := bin(expr.OpOr, bin(expr.OpAnd, eq(event, "push"), eq(branch, "main")), eq(event, "tag"))
	if drone.Guardable(push, exact) {
		t.Error("a push condition was guardable")
	}
	if c, ok := drone.FromExpr(push, exact); ok {
		t.Errorf("a push condition became a filter: %#v", c)
	}
	// Same key in both arms of an OR is a value list, so it stays a filter.
	same := bin(expr.OpOr, eq(event, "pull_request"), eq(event, "tag"))
	if c, ok := drone.FromExpr(same, exact); !ok || !reflect.DeepEqual(c.Event.Include, []string{"pull_request", "tag"}) {
		t.Errorf("same-key OR = %#v, %v", c, ok)
	}
	// A key repeated under AND with a different half has no single filter.
	twice := bin(expr.OpAnd, eq(event, "pull_request"), eq(event, "tag"))
	if c, ok := drone.FromExpr(twice, exact); ok {
		t.Errorf("two filters for one key merged: %#v", c)
	}
}

// Review Focus 2: Drone branch is the target branch on a pull request, so GHA branches maps without a caveat.
func TestBranchOnPullRequest(t *testing.T) {
	ghaPR := bin(expr.OpAnd, eq(event, "pull_request"), eq(branch, "main"))
	c, ok := drone.FromExpr(ghaPR, exact)
	want := drone.Conditions{Event: drone.Filter{Include: []string{"pull_request"}}, Branch: drone.Filter{Include: []string{"main"}}}
	if !ok || !reflect.DeepEqual(c, want) {
		t.Fatalf("FromExpr = %#v, %v; want %#v", c, ok, want)
	}
	e, err := drone.ToExpr(want)
	if err != nil || !reflect.DeepEqual(e, ghaPR) {
		t.Errorf("ToExpr = %#v, %v; want no tag guard and no caveat node", e, err)
	}
	for _, ref := range []expr.Ref{r("pr", "head_ref"), r("pr", "base_ref"), r("sha"), r("actor")} {
		if c, ok := drone.FromExpr(eq(ref, "x"), exact); ok {
			t.Errorf("%v has no Drone filter but gave %#v", ref.Path, c)
		}
	}
}

func TestFromExprRefusals(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"or across keys":  bin(expr.OpOr, eq(event, "push"), eq(branch, "main")),
		"inequality":      bin(expr.OpNe, event, str("push")),
		"number literal":  bin(expr.OpEq, event, expr.Lit{Kind: expr.LitNum, Num: 1}),
		"literal left":    bin(expr.OpEq, str("push"), event),
		"foreign opaque":  expr.Opaque{Dialect: "gha", Text: "x"},
		"glob of literal": expr.Opaque{Dialect: drone.Dialect, Text: `when.branch glob "main"`},
		"unknown call":    expr.Call{Name: "contains"},
		"status twice":    bin(expr.OpAnd, expr.Call{Name: "failure"}, expr.Call{Name: "always"}),
		"status argument": expr.Call{Name: "failure", Args: []expr.Expr{str("x")}},
		"env ref":         eq(r("env", "X"), "1"),
	} {
		if c, ok := drone.FromExpr(e, exact); ok {
			t.Errorf("%s: FromExpr = %#v; want a refusal", name, c)
		}
	}
}

func TestDisabledFilter(t *testing.T) {
	d := drone.Disabled()
	if !d.IsDisabled() || (drone.Conditions{}).IsDisabled() {
		t.Fatal("IsDisabled must hold for the disabled filter only")
	}
	if !reflect.DeepEqual(d.Event.Exclude, []string{"**"}) || len(d.Event.Include) != 0 {
		t.Errorf("disabled = %#v; want event exclude **", d)
	}
	got, ok := drone.FromExpr(expr.Lit{Kind: expr.LitBool}, exact)
	if !ok || !reflect.DeepEqual(got, d) {
		t.Errorf("false = %#v, %v; want the disabled filter", got, ok)
	}
	if got, ok := drone.FromExpr(expr.Lit{Kind: expr.LitBool, Bool: true}, exact); !ok || !reflect.DeepEqual(got, drone.Conditions{}) {
		t.Errorf("true = %#v, %v; want no conditions", got, ok)
	}
	parsed, _, err := drone.Parse([]byte("event:\n  exclude: ['**']\n"))
	if err != nil || !reflect.DeepEqual(parsed, d) {
		t.Errorf("Parse = %#v, %v", parsed, err)
	}
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     drone.Conditions
		rest     []string
	}{
		{"scalar", "branch: main", drone.Conditions{Branch: drone.Filter{Include: []string{"main"}}}, nil},
		{"list", "branch: [main, 'release/*']", drone.Conditions{Branch: drone.Filter{Include: []string{"main", "release/*"}}}, nil},
		{"map", "repo:\n  include: [a/b]\n  exclude: [a/c]",
			drone.Conditions{Repo: drone.Filter{Include: []string{"a/b"}, Exclude: []string{"a/c"}}}, nil},
		{"exclude only", "branch:\n  exclude: [wip]", drone.Conditions{Branch: drone.Filter{Exclude: []string{"wip"}}}, nil},
		{"empty list matches all", "branch: []", drone.Conditions{}, nil},
		{"status failure", "status: failure", drone.Conditions{Status: []string{"failure"}}, nil},
		{"status both", "status: [failure, success]", drone.Conditions{Status: []string{"success", "failure"}}, nil},
		{"status success is default", "status: [success]", drone.Conditions{}, nil},
		{"unmodelled keys", "paths: [a]\ncron: nightly\nevent: tag\ntarget: x",
			drone.Conditions{Event: drone.Filter{Include: []string{"tag"}}}, []string{"cron", "paths", "target"}},
		{"alias", "a: &x [main]\nbranch: *x", drone.Conditions{Branch: drone.Filter{Include: []string{"main"}}}, []string{"a"}},
		{"number as text", "branch: 1", drone.Conditions{Branch: drone.Filter{Include: []string{"1"}}}, nil},
		{"empty document", "", drone.Conditions{}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, rest, err := drone.Parse([]byte(c.in))
			if err != nil || !reflect.DeepEqual(got, c.want) || !reflect.DeepEqual(rest, c.rest) {
				t.Errorf("Parse = %#v, %v, %v; want %#v, %v", got, rest, err, c.want, c.rest)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"scalar include drops silently in Drone": "branch:\n  include: main",
		"null value":                             "branch:",
		"nested map":                             "branch: {a: {b: c}}",
		"unknown map key":                        "branch: {match: [a]}",
		"repeated map key":                       "branch: {include: [a], include: [b]}",
		"list of lists":                          "branch: [[a]]",
		"null in list":                           "branch: [~]",
		"status unknown":                         "status: skipped",
		"status exclude":                         "status: {exclude: [failure]}",
		"not a mapping":                          "- a",
		"bad yaml":                               "branch: [",
	} {
		if got, _, err := drone.Parse([]byte(in)); err == nil {
			t.Errorf("%s: Parse = %#v; want an error", name, got)
		}
	}
}

func genFilter(t *rapid.T, label string) drone.Filter {
	val := rapid.SampledFrom([]string{"main", "release/*", "refs/tags/v1", "refs/tags/v*", "refs/heads/x", "a b", "**", "", "x'y", "[ab]"})
	var f drone.Filter
	f.Include = rapid.SliceOfN(val, 0, 3).Draw(t, label+"inc")
	f.Exclude = rapid.SliceOfN(val, 0, 3).Draw(t, label+"exc")
	if len(f.Include) == 0 {
		f.Include = nil
	}
	if len(f.Exclude) == 0 {
		f.Exclude = nil
	}
	return f
}

func genEventFilter(t *rapid.T) drone.Filter {
	val := rapid.SampledFrom([]string{"pull_request", "cron", "custom", "tag", "promote", "rollback", "p*", "**"})
	f := drone.Filter{Include: rapid.SliceOfN(val, 0, 3).Draw(t, "eventinc"), Exclude: rapid.SliceOfN(val, 0, 3).Draw(t, "eventexc")}
	if len(f.Include) == 0 {
		f.Include = nil
	}
	if len(f.Exclude) == 0 {
		f.Exclude = nil
	}
	return f
}

func genConditions(t *rapid.T) drone.Conditions {
	return drone.Conditions{
		Event: genEventFilter(t), Branch: genFilter(t, "branch"), Ref: genFilter(t, "ref"), Repo: genFilter(t, "repo"),
		Status: rapid.SampledFrom([][]string{nil, {"failure"}, {"success", "failure"}}).Draw(t, "status"),
	}
}

func TestConditionsRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genConditions(t)
		out, err := yaml.Marshal(c.YAML())
		if err != nil {
			t.Fatal(err)
		}
		back, rest, err := drone.Parse(out)
		if err != nil || len(rest) != 0 || !reflect.DeepEqual(back, c) {
			t.Fatalf("YAML round trip of %#v via %q gave %#v, %v, %v", c, out, back, rest, err)
		}
		e, err := drone.ToExpr(c)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := drone.FromExpr(e, exact)
		if !ok || !reflect.DeepEqual(got, c) {
			t.Fatalf("expression round trip of %#v gave %#v, %v", c, got, ok)
		}
	})
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"branch: main", "branch: [a, 'b/*']", "event: {exclude: ['**']}", "status: [failure]",
		"repo: {include: [a/b], exclude: [a/c]}", "paths: [x]", "a: &x [m]\nbranch: *x", "ref: refs/tags/v1", "", "~", "[",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c, _, err := drone.Parse(data)
		if err != nil {
			return
		}
		out, err := yaml.Marshal(c.YAML())
		if err != nil {
			t.Fatalf("marshal of %#v: %v", c, err)
		}
		back, _, err := drone.Parse(out)
		if err != nil || !reflect.DeepEqual(back, c) {
			t.Fatalf("YAML round trip of %#v via %q gave %#v, %v", c, out, back, err)
		}
		e, err := drone.ToExpr(c)
		if err != nil {
			return // an event value with no canonical name, such as push
		}
		if got, ok := drone.FromExpr(e, exact); !ok || !reflect.DeepEqual(got, c) {
			t.Fatalf("expression round trip of %#v gave %#v, %v", c, got, ok)
		}
	})
}

func TestEventsTranslate(t *testing.T) {
	for _, c := range []struct{ native, canon string }{
		{"cron", "schedule"}, {"custom", "workflow_dispatch"}, {"pull_request", "pull_request"},
		{"tag", "tag"}, {"promote", "promote"}, {"rollback", "rollback"},
	} {
		inc, err := drone.ToExpr(drone.Conditions{Event: drone.Filter{Include: []string{c.native}}})
		if err != nil || !reflect.DeepEqual(inc, eq(event, c.canon)) {
			t.Errorf("ToExpr(event %s) = %#v, %v; want event == %q", c.native, inc, err, c.canon)
		}
		got, ok := drone.FromExpr(eq(event, c.canon), exact)
		if !ok || !reflect.DeepEqual(got.Event.Include, []string{c.native}) {
			t.Errorf("FromExpr(event == %q) = %#v, %v; want %s", c.canon, got, ok, c.native)
		}
		exc, ok := drone.FromExpr(not(eq(event, c.canon)), exact)
		if !ok || !reflect.DeepEqual(exc.Event.Exclude, []string{c.native}) {
			t.Errorf("FromExpr(event != %q) = %#v, %v; want exclude %s", c.canon, exc, ok, c.native)
		}
	}
}

func TestEventsWithoutDroneLiteral(t *testing.T) {
	for _, v := range []string{"push", "foo", "cron-ish"} {
		if c, ok := drone.FromExpr(eq(event, v), exact); ok {
			t.Errorf("FromExpr(event == %q) = %#v; want a refusal", v, c)
		}
	}
	// Native names with no canonical meaning cannot stand in the canonical AST.
	for _, v := range []string{"push", "schedule", "workflow_dispatch", "foo"} {
		if e, err := drone.ToExpr(drone.Conditions{Event: drone.Filter{Include: []string{v}}}); err == nil {
			t.Errorf("ToExpr(event %s) = %#v; want an error", v, e)
		}
	}
	if c, ok := drone.FromExpr(bin(expr.OpEq, event, r("branch")), exact); ok {
		t.Errorf("event against a ref became a filter: %#v", c)
	}
}

// A GHA-style source folds case in ==, and Drone globs do not: only a value with no cased character agrees.
func TestFoldingSourceFilters(t *testing.T) {
	if c, ok := drone.FromExpr(eq(branch, "Main"), folding); ok {
		t.Errorf("a folded branch comparison became a case-sensitive filter: %#v", c)
	}
	if _, ok := drone.FromExpr(eq(branch, "Main"), exact); !ok {
		t.Error("an exact source keeps the filter")
	}
	for _, e := range []expr.Expr{eq(branch, "main"), eq(r("tag"), "v1"), eq(r("repo"), "a/b")} {
		if c, ok := drone.FromExpr(e, folding); ok {
			t.Errorf("%#v became a filter under folding: %#v", e, c)
		}
	}
	if c, ok := drone.FromExpr(eq(branch, "2024-01"), folding); !ok || !reflect.DeepEqual(c.Branch.Include, []string{"2024-01"}) {
		t.Errorf("an uncased value stays exact: %#v, %v", c, ok)
	}
	// Event values are lowercase on both sides, so the literal is folded before the table lookup.
	if c, ok := drone.FromExpr(eq(event, "Schedule"), folding); !ok || !reflect.DeepEqual(c.Event.Include, []string{"cron"}) {
		t.Errorf("folded event = %#v, %v; want cron", c, ok)
	}
	if c, ok := drone.FromExpr(eq(event, "Schedule"), exact); ok {
		t.Errorf("an exact source matched a cased event: %#v", c)
	}
}
