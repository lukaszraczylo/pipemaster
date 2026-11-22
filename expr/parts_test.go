package expr_test

import (
	"reflect"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
)

func pref(p ...string) expr.Ref { return expr.Ref{Path: p} }

func TestMergeText(t *testing.T) {
	got := expr.MergeText([]expr.Part{{Text: "a"}, {Text: "b"}, {Expr: ref("inputs", "x")}, {Text: "c"}, {Text: "d"}})
	want := []expr.Part{{Text: "ab"}, {Expr: ref("inputs", "x")}, {Text: "cd"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestJoinParts(t *testing.T) {
	x := ref("inputs", "x")
	if got := expr.JoinParts([]expr.Part{{Expr: x}}); !reflect.DeepEqual(got, x) {
		t.Fatalf("lone ref = %v", got)
	}
	got := expr.String(expr.JoinParts([]expr.Part{{Text: "v{"}, {Expr: x}, {Text: "}"}}))
	if want := "format('v{{{0}}}', inputs.x)"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestNeedRefsAndRename(t *testing.T) {
	e := expr.Binary{Op: expr.OpAnd, L: expr.Binary{Op: expr.OpEq, L: ref("needs", "a", "result"), R: expr.Lit{Kind: expr.LitStr, Str: "success"}},
		R: expr.Unary{Op: expr.OpNot, X: expr.Call{Name: "f", Args: []expr.Expr{ref("needs", "b", "outputs", "o")}}}}
	var seen []string
	expr.NeedRefs(e, func(n string) { seen = append(seen, n) })
	if !reflect.DeepEqual(seen, []string{"a", "b"}) {
		t.Fatalf("seen %v", seen)
	}
	renamed := expr.RenameNeeds(e, func(n string) string { return n + "2" })
	seen = nil
	expr.NeedRefs(renamed, func(n string) { seen = append(seen, n) })
	if !reflect.DeepEqual(seen, []string{"a2", "b2"}) {
		t.Fatalf("renamed %v", seen)
	}
	if !reflect.DeepEqual(e.L.(expr.Binary).L, ref("needs", "a", "result")) {
		t.Fatal("RenameNeeds changed its input")
	}
}
