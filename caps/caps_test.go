package caps

import (
	"sort"
	"testing"
)

func TestLevelOrder(t *testing.T) {
	if Native >= Emulated || Emulated >= External || External >= Lost {
		t.Fatal("level order broken")
	}
}

func TestLevelRoundTrip(t *testing.T) {
	for _, l := range []Level{Native, Emulated, External, Lost} {
		got, err := ParseLevel(l.String())
		if err != nil || got != l {
			t.Fatalf("round trip %v: got %v err %v", l, got, err)
		}
		b, err := l.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var u Level
		if err := u.UnmarshalText(b); err != nil || u != l {
			t.Fatalf("text round trip %v: got %v err %v", l, u, err)
		}
	}
	if _, err := ParseLevel("x"); err == nil {
		t.Fatal("expected error for unknown level")
	}
	var u Level
	if err := u.UnmarshalText([]byte("x")); err == nil {
		t.Fatal("expected error for unknown text")
	}
}

func TestLookupMissingIsLost(t *testing.T) {
	got := Matrix{}.Lookup(JobMatrix)
	if got.Level != Lost || got.Strategy != "" || got.Caveat != "not declared by target" {
		t.Fatalf("unexpected %+v", got)
	}
	m := Matrix{JobMatrix: {Level: Native}}
	if m.Lookup(JobMatrix).Level != Native {
		t.Fatal("declared feature not returned")
	}
}

func TestAllFeaturesSortedUnique(t *testing.T) {
	fs := AllFeatures()
	if len(fs) != 45 {
		t.Fatalf("want 45 features, got %d", len(fs))
	}
	if !sort.SliceIsSorted(fs, func(i, j int) bool { return fs[i] < fs[j] }) {
		t.Fatal("not sorted")
	}
	seen := map[Feature]bool{}
	for _, f := range fs {
		if seen[f] {
			t.Fatalf("duplicate %s", f)
		}
		seen[f] = true
	}
}
