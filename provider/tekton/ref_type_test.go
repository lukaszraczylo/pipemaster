package tekton_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/expr"
)

func tagPrefix(prefix string) expr.Expr {
	return expr.Call{Name: "startsWith", Args: []expr.Expr{expr.Ref{Path: []string{expr.CtxRef}}, expr.Lit{Kind: expr.LitStr, Str: prefix}}}
}

// A tag prefix test of the ref is written as the test of the ref type, and the marker gives the prefix test back.
func TestTagPrefixConditionIsWrittenAsRefTypeAndComesBack(t *testing.T) {
	cases := []struct {
		cond    expr.Expr
		name    string
		refType bool
	}{
		{tagPrefix("refs/tags/"), "tag prefix", true},
		{expr.Unary{Op: expr.OpNot, X: tagPrefix("refs/tags/")}, "negated tag prefix", true},
		{tagPrefix("refs/tags/v"), "a longer tag prefix has no ref type form", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := scriptJob("deploy", "echo hi")
			j.If = c.cond
			files, rep := emit(t, project(j))
			if got := strings.Contains(joinFiles(files), "ctx-ref-type"); got != c.refType {
				t.Fatalf("ref type test present = %v, want %v:\n%s", got, c.refType, joinFiles(files))
			}
			if !c.refType {
				return
			}
			for _, e := range rep.Entries {
				if e.Feature == caps.JobIf && e.Level == caps.Lost {
					t.Fatalf("the condition is lost: %+v", e)
				}
			}
			back, _ := reparse(t, files)
			got := back.Pipelines[0].Jobs[0].If
			if got == nil || expr.String(got) != expr.String(c.cond) {
				t.Errorf("round trip = %v, want %s", got, expr.String(c.cond))
			}
		})
	}
}
