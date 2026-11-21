package shguard_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/expr"
	"github.com/lukaszraczylo/pipemaster/expr/internal/shguard"
)

var dialect = shguard.Dialect{
	Name: func(r expr.Ref) (string, bool) { return strings.ToUpper(strings.Join(r.Path, "_")), len(r.Path) > 0 },
	Ref: func(n string) (expr.Ref, bool) {
		return expr.Ref{Path: strings.Split(strings.ToLower(n), "_")}, true
	},
}

func eq(op, name, lit string) expr.Expr {
	return expr.Binary{Op: op, L: expr.Ref{Path: []string{name}}, R: expr.Lit{Kind: expr.LitStr, Str: lit}}
}

func TestRoundTrip(t *testing.T) {
	for name, e := range map[string]expr.Expr{
		"eq":      eq(expr.OpEq, "a", "x"),
		"quote":   eq(expr.OpEq, "a", "it's"),
		"hostile": eq(expr.OpNe, "a", "$(id) `x` \"q\" a b"),
		"and/or":  expr.Binary{Op: expr.OpOr, L: eq(expr.OpEq, "a", "1"), R: expr.Unary{Op: expr.OpNot, X: eq(expr.OpEq, "b", "2")}},
	} {
		line, err := dialect.Print(e)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if back, ok := dialect.Parse(line); !ok || !reflect.DeepEqual(back, e) {
			t.Errorf("%s: Parse(%q) = %#v, %v", name, line, back, ok)
		}
	}
}

func TestRefusals(t *testing.T) {
	if _, err := dialect.Print(expr.Call{Name: "success"}); !errors.Is(err, shguard.ErrNotGuardable) {
		t.Errorf("call: %v", err)
	}
	if dialect.Guardable(eq(expr.OpEq, "a", "x\ny")) {
		t.Error("newline literal guardable")
	}
	if _, ok := dialect.Parse(`if ! [ "$A" = 'x' ]; then exit 0; fi`); ok {
		t.Error("parsed a line without the marker")
	}
}

func TestAddRemove(t *testing.T) {
	script := []string{"echo hi"}
	g, err := dialect.Add(script, eq(expr.OpEq, "a", "x"))
	if err != nil || len(g) != 2 {
		t.Fatalf("Add = %q, %v", g, err)
	}
	rest, e, ok := dialect.Remove(g)
	if !ok || !reflect.DeepEqual(rest, script) || e == nil {
		t.Errorf("Remove = %q, %v, %v", rest, e, ok)
	}
}
