package concourse_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	exprconcourse "github.com/lukaszraczylo/pipemaster/expr/concourse"
)

func branchIs(v string) expr.Expr {
	return expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: expr.Lit{Kind: expr.LitStr, Str: v}}
}

func TestGuardRoundTrip(t *testing.T) {
	e := branchIs("main")
	line, err := exprconcourse.PrintGuard(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"$PM_CTX_BRANCH"`) || !strings.HasSuffix(line, exprconcourse.GuardMarker) {
		t.Fatalf("guard line %q", line)
	}
	back, ok := exprconcourse.ParseGuard(line)
	if !ok || expr.String(back) != expr.String(e) {
		t.Fatalf("parse back = %v %v", back, ok)
	}
	refs, ok := exprconcourse.GuardRefs(e)
	if !ok || len(refs) != 1 || expr.String(refs[0]) != "branch" {
		t.Fatalf("refs = %v %v", refs, ok)
	}
}

func TestGuardRefusesWhatItCannotRead(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"opaque":      expr.Opaque{Dialect: "gha", Text: "always()"},
		"needs ref":   expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.PrefixNeeds, "a", "outputs", "x"}}, R: expr.Lit{Kind: expr.LitStr, Str: "1"}},
		"upper input": expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.PrefixInputs, "Foo"}}, R: expr.Lit{Kind: expr.LitStr, Str: "1"}},
	} {
		if exprconcourse.Guardable(e) {
			t.Errorf("%s is guardable", name)
		}
	}
}

func TestParseGuardRejectsEdit(t *testing.T) {
	line, _ := exprconcourse.PrintGuard(branchIs("main"))
	if _, ok := exprconcourse.ParseGuard(strings.Replace(line, "main", "dev", 1)); !ok {
		t.Fatal("an edited literal is still a guard that Print writes")
	}
	if _, ok := exprconcourse.ParseGuard(strings.Replace(line, "PM_CTX_BRANCH", "OTHER", 1)); ok {
		t.Fatal("an unknown variable must not parse")
	}
}
