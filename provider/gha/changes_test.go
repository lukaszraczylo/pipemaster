package gha

import (
	"regexp"
	"testing"
)

// TestNegatedClassNeverMatchesASlash: fnmatch with FNM_PATHNAME does not let a class match a slash.
func TestNegatedClassNeverMatchesASlash(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"a[!b]c", "axc", true},
		{"a[!b]c", "a/c", false},
		{"a[!b]c", "abc", false},
		{"a[^b]c", "a/c", false},
		{"a[b-d]c", "acc", true},
		{"a[b-d]c", "aec", false},
		{"**/*.go", "x.go", true},
		{"**/*.go", "a/b/x.go", true},
		{"*.go", "a/x.go", false},
		{"{a,b}.txt", "b.txt", true},
	}
	for _, c := range cases {
		re, ok := atomERE([]string{c.glob})
		if !ok {
			t.Fatalf("%q has no regex", c.glob)
		}
		if got := regexp.MustCompile(re).MatchString(c.path); got != c.want {
			t.Errorf("%q on %q = %v, want %v (%s)", c.glob, c.path, got, c.want, re)
		}
	}
}

func TestGlobsOutsideTheAllowListHaveNoRegex(t *testing.T) {
	for _, g := range []string{"a[';x]b", "a$b", "a`b`", "a'b", `a\b`, "é.go", "a[]]b", "a[/]b", "a[!]b", "a[--/]b"} {
		if _, ok := atomERE([]string{g}); ok {
			t.Errorf("%q must not translate", g)
		}
	}
}
