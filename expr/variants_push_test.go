package expr_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/gha"
	"github.com/lukaszraczylo/pipemaster/expr/gitlab"
	"github.com/lukaszraczylo/pipemaster/expr/woodpecker"
)

func eventLit(s string) expr.Expr {
	return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxEvent}}, R: expr.Lit{Kind: expr.LitStr, Str: s}}
}

// A push test of a source whose push covers a tag push becomes the push and tag events of Woodpecker, and its
// complement the other events of the source. A test the target would answer for events the source never starts has no
// form, and no sentinel of a variant stays in a result.
func TestPushVariantForWoodpecker(t *testing.T) {
	cases := []struct {
		cond   expr.Expr
		want   []string
		name   string
		from   expr.Dialect
		refuse bool
	}{
		{name: "gitlab push", from: gitlab.Lang, cond: eventLit("push"), want: []string{`CI_PIPELINE_EVENT == "push"`, `CI_PIPELINE_EVENT == "tag"`}},
		{name: "gha push", from: gha.Lang, cond: eventLit("push"), want: []string{`CI_PIPELINE_EVENT == "push"`, `CI_PIPELINE_EVENT == "tag"`}},
		{name: "gitlab not push", from: gitlab.Lang,
			cond: expr.Unary{Op: expr.OpNot, X: eventLit("push")}, want: []string{"pull_request", "cron", "manual"}},
		{name: "push under a negated conjunction", from: gitlab.Lang, refuse: true,
			cond: expr.Unary{Op: expr.OpNot, X: expr.Binary{Op: expr.OpAnd, L: eventLit("push"), R: eventLit("schedule")}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := expr.TranslateFacts(c.cond, c.from, woodpecker.Lang, expr.Facts{})
			if c.refuse {
				if ok {
					t.Fatalf("translated to %v", got)
				}
				return
			}
			if !ok {
				t.Fatal("not translated")
			}
			text, err := woodpecker.Print(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.want {
				if !strings.Contains(text, w) {
					t.Errorf("%q is missing from %q", w, text)
				}
			}
			if strings.Contains(strings.ToLower(text), "pipemaster") {
				t.Errorf("a sentinel stays in %q", text)
			}
			if strings.HasPrefix(c.name, "gitlab not") || strings.HasPrefix(c.name, "drone not") {
				for _, no := range []string{`"push"`, `"tag"`, "release"} {
					if strings.Contains(text, no) {
						t.Errorf("%s is admitted by %q", no, text)
					}
				}
			}
		})
	}
}

// A positive test of the GitLab api or trigger source is a repository_dispatch event of that type for GHA. A negative
// test has no form: a trigger run passes it in GitLab and the event type of the run is not known to the target.
func TestAPISourceVariantForGHA(t *testing.T) {
	for _, src := range []string{"api", "trigger"} {
		got, _, ok := expr.TranslateApprox(eventLit(src), gitlab.Lang, gha.Lang)
		if !ok {
			t.Fatalf("%s: not translated", src)
		}
		text, err := gha.Print(got)
		if err != nil {
			t.Fatal(err)
		}
		want := "github.event_name == 'repository_dispatch' && github.event.action == '" + src + "'"
		if text != want {
			t.Errorf("%s: got %q, want %q", src, text, want)
		}
	}
	for name, cond := range map[string]expr.Expr{
		"not api": expr.Unary{Op: expr.OpNot, X: eventLit("api")},
		"ne api":  expr.Binary{Op: expr.OpNe, L: expr.Ref{Path: []string{expr.CtxEvent}}, R: expr.Lit{Kind: expr.LitStr, Str: "api"}},
	} {
		if got, _, ok := expr.TranslateApprox(cond, gitlab.Lang, gha.Lang); ok {
			t.Errorf("%s translated to %v", name, got)
		}
	}
}
