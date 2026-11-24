package expr

import "testing"

func TestChangesAtomsDeduplicate(t *testing.T) {
	a, b := Changes("go.mod", "**/*.go"), Changes("Makefile")
	x := Binary{Op: OpOr, L: Binary{Op: OpAnd, L: a, R: Ref{Path: []string{"branch"}}}, R: Binary{Op: OpAnd, L: Unary{Op: OpNot, X: a}, R: b}}
	atoms := ChangesAtoms(x)
	if len(atoms) != 2 || String(atoms[0]) != String(a) || String(atoms[1]) != String(b) {
		t.Fatalf("atoms = %v, want [%s %s] once each", atoms, String(a), String(b))
	}
	if !HasChanges(x) || HasChanges(Ref{Path: []string{"branch"}}) {
		t.Fatal("HasChanges wrong")
	}
	globs, ok := ChangesGlobs(a)
	if !ok || len(globs) != 2 || globs[1] != "**/*.go" {
		t.Fatalf("globs = %v %v", globs, ok)
	}
	if _, ok := ChangesGlobs(Ref{Path: []string{"branch"}}); ok {
		t.Fatal("a ref is not a changes atom")
	}
}

func TestAssignChangesSimplifies(t *testing.T) {
	a, b := Changes("a"), Changes("b")
	br := Ref{Path: []string{"branch"}}
	x := Binary{Op: OpOr, L: Binary{Op: OpAnd, L: br, R: a}, R: Binary{Op: OpAnd, L: Unary{Op: OpNot, X: Binary{Op: OpAnd, L: br, R: a}}, R: b}}
	got := String(AssignChanges(x, map[string]bool{String(a): true, String(b): false}))
	if got != String(br) {
		t.Fatalf("a=true b=false: %s, want %s", got, String(br))
	}
	if l, ok := AssignChanges(x, map[string]bool{String(a): false, String(b): false}).(Lit); !ok || l.Kind != LitBool || l.Bool {
		t.Fatalf("a=false b=false must fold to false")
	}
	if got := String(AssignChanges(x, map[string]bool{String(a): false, String(b): true})); got != "true" {
		t.Fatalf("a=false b=true folded to %s, want true", got)
	}
	if got := String(AssignChanges(x, map[string]bool{String(a): true})); !HasChanges(AssignChanges(x, map[string]bool{String(a): true})) || got == "" {
		t.Fatalf("an unassigned atom must stay: %s", got)
	}
}

func TestChangesCanonicalRoundTrip(t *testing.T) {
	x := Binary{Op: OpAnd, L: Changes("a b", "c'd"), R: Ref{Path: []string{"branch"}}}
	if err := Check(x); err != nil {
		t.Fatal(err)
	}
	back, err := ParseCanonical(String(x))
	if err != nil || String(back) != String(x) {
		t.Fatalf("round trip %v %v", back, err)
	}
}
