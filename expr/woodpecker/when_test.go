package woodpecker_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
)

func eq(ref expr.Expr, v string) expr.Expr { return bin(expr.OpEq, ref, str(v)) }

var tagGuard = eq(r("event"), "tag")

func TestFilterExpr(t *testing.T) {
	glob := woodpecker.GlobOpaque("branch", "release/*")
	for _, c := range []struct {
		want expr.Expr
		name string
		key  string
		f    woodpecker.Filter
	}{
		{nil, "empty matches all", "event", woodpecker.Filter{}},
		{eq(r("event"), "push"), "single literal", "event", woodpecker.Filter{Include: []string{"push"}}},
		{bin(expr.OpOr, eq(r("event"), "push"), eq(r("event"), "tag")), "list is OR", "event",
			woodpecker.Filter{Include: []string{"push", "tag"}}},
		{bin(expr.OpAnd, eq(r("repo"), "a/b"), not(eq(r("repo"), "a/c"))), "include and exclude", "repo",
			woodpecker.Filter{Include: []string{"a/b"}, Exclude: []string{"a/c"}}},
		{not(eq(r("repo"), "a/c")), "exclude only", "repo", woodpecker.Filter{Exclude: []string{"a/c"}}},
		{bin(expr.OpOr, tagGuard, eq(branch, "main")), "branch skips tag events", "branch",
			woodpecker.Filter{Include: []string{"main"}}},
		// Review Focus 4: release/* is doublestar (`*` stops at `/`), not startsWith("release/").
		{bin(expr.OpOr, tagGuard, glob), "glob stays opaque", "branch", woodpecker.Filter{Include: []string{"release/*"}}},
		{bin(expr.OpOr, tagGuard, bin(expr.OpOr, eq(branch, "main"), glob)), "glob among literals", "branch",
			woodpecker.Filter{Include: []string{"main", "release/*"}}},
		{eq(r("woodpecker", "CI_SYSTEM_HOST"), "ci.example.org"), "native ref", "instance",
			woodpecker.Filter{Include: []string{"ci.example.org"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := woodpecker.FilterExpr(c.key, c.f)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("FilterExpr = %#v, %v; want %#v", got, err, c.want)
			}
		})
	}
	if _, err := woodpecker.FilterExpr("path", woodpecker.Filter{Include: []string{"x"}}); err == nil {
		t.Error("path has no expression form")
	}
}

func TestGlobNeverBecomesPrefix(t *testing.T) {
	e, err := woodpecker.FilterExpr("branch", woodpecker.Filter{Include: []string{"release/*"}})
	if err != nil || !expr.HasOpaque(e) {
		t.Fatalf("FilterExpr = %#v, %v; want an Opaque glob", e, err)
	}
	if _, err := woodpecker.Print(e); err == nil {
		t.Error("a glob printed as expression text")
	}
	for _, e := range []expr.Expr{e} {
		var hasCall func(expr.Expr) bool
		hasCall = func(x expr.Expr) bool {
			switch v := x.(type) {
			case expr.Call:
				return true
			case expr.Binary:
				return hasCall(v.L) || hasCall(v.R)
			case expr.Unary:
				return hasCall(v.X)
			}
			return false
		}
		if hasCall(e) {
			t.Error("glob translated to a call such as startsWith")
		}
	}
}

func TestIsLiteral(t *testing.T) {
	for v, want := range map[string]bool{
		"main": true, "feat/x-1.2_3": true, "": true, "a b": true, "refs/tags/v1": true,
		"release/*": false, "**": false, "a?": false, "[ab]": false, "{a,b}": false, `a\b`: false, "a]": false, "a}": false,
	} {
		if got := woodpecker.IsLiteral(v); got != want {
			t.Errorf("IsLiteral(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestGlobOpaqueRoundTrip(t *testing.T) {
	o := woodpecker.GlobOpaque("ref", `refs/tags/v*"x`)
	k, p, ok := woodpecker.ParseGlob(o)
	if !ok || k != "ref" || p != `refs/tags/v*"x` {
		t.Errorf("ParseGlob = %q, %q, %v", k, p, ok)
	}
	for _, bad := range []expr.Opaque{
		{Dialect: "gha", Text: o.Text},
		{Dialect: "woodpecker", Text: `when.ref glob "plain"`},
		{Dialect: "woodpecker", Text: `when.nokey glob "a*"`},
		{Dialect: "woodpecker", Text: `when.ref glob a*`},
		{Dialect: "woodpecker", Text: `CI_X == 1`},
	} {
		if _, _, ok := woodpecker.ParseGlob(bad); ok {
			t.Errorf("ParseGlob(%#v) accepted", bad)
		}
	}
}

// Review Focus 2: items are OR, keys in one item are AND.
func TestWhenListIsOrKeysAreAnd(t *testing.T) {
	got, err := woodpecker.WhenExpr([]woodpecker.Item{
		{Event: woodpecker.Filter{Include: []string{"push"}}, Branch: woodpecker.Filter{Include: []string{"main"}}},
		{Event: woodpecker.Filter{Include: []string{"tag"}}},
	})
	want := bin(expr.OpOr,
		bin(expr.OpAnd, eq(r("event"), "push"), bin(expr.OpOr, tagGuard, eq(branch, "main"))),
		eq(r("event"), "tag"))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("WhenExpr = %#v, %v; want %#v", got, err, want)
	}
}

func TestEvaluateAndsWithFilters(t *testing.T) {
	got, err := woodpecker.ItemExpr(woodpecker.Item{
		Event: woodpecker.Filter{Include: []string{"push"}}, Evaluate: `SKIP != "true"`,
	})
	want := bin(expr.OpAnd, eq(r("event"), "push"), bin(expr.OpNe, r("env", "SKIP"), str("true")))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ItemExpr = %#v, %v; want %#v", got, err, want)
	}
	if _, err := woodpecker.ItemExpr(woodpecker.Item{Evaluate: `A ==`}); err == nil {
		t.Error("a malformed evaluate must fail")
	}
}

func TestEmptyWhenMatchesAll(t *testing.T) {
	for name, items := range map[string][]woodpecker.Item{
		"no items": nil, "empty item": {{}}, "empty among others": {{Event: woodpecker.Filter{Include: []string{"push"}}}, {}},
	} {
		if got, err := woodpecker.WhenExpr(items); err != nil || got != nil {
			t.Errorf("%s: WhenExpr = %#v, %v; want nil", name, got, err)
		}
	}
}

func TestSplitInvertsWhen(t *testing.T) {
	glob := woodpecker.Filter{Include: []string{"main", "release/*"}}
	for name, items := range map[string][]woodpecker.Item{
		"event list":      {{Event: woodpecker.Filter{Include: []string{"push", "tag"}}}},
		"and of keys":     {{Event: woodpecker.Filter{Include: []string{"push"}}, Branch: woodpecker.Filter{Include: []string{"main"}}, Repo: woodpecker.Filter{Exclude: []string{"a/b"}}}},
		"two items":       {{Event: woodpecker.Filter{Include: []string{"push"}}, Branch: woodpecker.Filter{Include: []string{"main"}}}, {Event: woodpecker.Filter{Include: []string{"tag"}}}},
		"include+exclude": {{Ref: woodpecker.Filter{Include: []string{"refs/heads/x"}, Exclude: []string{"refs/heads/y"}}}},
		"glob in branch":  {{Event: woodpecker.Filter{Include: []string{"push"}}, Branch: glob}},
		"with evaluate":   {{Event: woodpecker.Filter{Include: []string{"push"}}, Evaluate: `SKIP != "true"`}},
		"instance":        {{Instance: woodpecker.Filter{Include: []string{"ci.example.org"}}}},
		"exclude only":    {{Branch: woodpecker.Filter{Exclude: []string{"wip"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			e, err := woodpecker.WhenExpr(items)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := woodpecker.SplitWhen(e)
			if !ok || !reflect.DeepEqual(got, items) {
				t.Errorf("SplitWhen = %#v, %v; want %#v", got, ok, items)
			}
		})
	}
}

func TestSplitKeepsUnguardedBranchAsEvaluate(t *testing.T) {
	// `CI_COMMIT_BRANCH == "main"` also fails on a tag event, unlike the branch filter, so it stays an evaluate.
	got, ok := woodpecker.SplitItem(eq(branch, "main"))
	if !ok || !got.Branch.Empty() || got.Evaluate != `CI_COMMIT_BRANCH == "main"` {
		t.Errorf("SplitItem = %#v, %v", got, ok)
	}
	it, ok := woodpecker.SplitItem(bin(expr.OpAnd, eq(r("event"), "push"), woodpecker.GlobOpaque("repo", "a/*")))
	if !ok || !reflect.DeepEqual(it.Repo, woodpecker.Filter{Include: []string{"a/*"}}) {
		t.Errorf("a repo glob must split as a repo filter: %#v, %v", it, ok)
	}
	if _, ok := woodpecker.SplitItem(bin(expr.OpOr, eq(branch, "a"), woodpecker.GlobOpaque("branch", "r/*"))); ok {
		t.Error("an unguarded glob chain has no evaluate text and must not split")
	}
}

func TestStatus(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string
	}{
		{"success", nil}, {"success", []string{"success"}}, {"failure", []string{"failure"}},
		{"always", []string{"success", "failure"}}, {"always", []string{"failure", "success"}},
	} {
		e, err := woodpecker.StatusExpr(c.in)
		if want := call(c.name); err != nil || !reflect.DeepEqual(e, want) {
			t.Errorf("StatusExpr(%v) = %#v, %v; want %s()", c.in, e, err, c.name)
		}
	}
	if _, err := woodpecker.StatusExpr([]string{"skipped"}); err == nil {
		t.Error("unknown status accepted")
	}
	for name, want := range map[string][]string{"success": nil, "failure": {"failure"}, "always": {"success", "failure"}} {
		got, ok := woodpecker.StatusList(call(name))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("StatusList(%s) = %v, %v", name, got, ok)
		}
	}
	if _, ok := woodpecker.StatusList(call("cancelled")); ok {
		t.Error("cancelled has no woodpecker status")
	}
}
