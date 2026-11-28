package drone

import (
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
)

func TestChangesGuardRoundTripsAsResultNode(t *testing.T) {
	atom := expr.Changes("**/*.go", "go.mod")
	cond := expr.Binary{Op: expr.OpAnd, L: expr.Binary{Op: expr.OpEq, L: expr.Ref{Path: []string{expr.CtxBranch}}, R: expr.Lit{Kind: expr.LitStr, Str: "main"}}, R: atom}
	line, err := PrintGuard(cond, Semantics)
	if err != nil {
		t.Fatal(err)
	}
	back, ok := ParseGuard(line)
	if !ok {
		t.Fatalf("the guard does not parse back: %s", line)
	}
	again, err := PrintGuard(back, Semantics)
	if err != nil || again != line {
		t.Fatalf("print of the parsed guard = %q, %v; want %q", again, err, line)
	}
	if !Guardable(expr.Unary{Op: expr.OpNot, X: atom}, Semantics) {
		t.Error("a negated path predicate has a guard")
	}
}

func TestChangesGuardRefusesForeignResultNames(t *testing.T) {
	for _, name := range []string{"", "x", "c12345678a", "cZZZZZZZZ", "../../etc"} {
		node := expr.Call{Name: ChangesResultCall, Args: []expr.Expr{expr.Lit{Kind: expr.LitStr, Str: name}}}
		if Guardable(node, Semantics) {
			t.Errorf("result name %q is guardable", name)
		}
	}
}
