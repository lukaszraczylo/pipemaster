package emitkit

import (
	"strings"
	"testing"
)

func TestValidNodeLabel(t *testing.T) {
	name63 := strings.Repeat("a", 63)
	prefix253 := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(prefix253) != 253 {
		t.Fatalf("test prefix has %d characters, want 253", len(prefix253))
	}
	tests := []struct {
		name string
		k, v string
		want bool
	}{
		{"plain key and value", "region", "eu", true},
		{"empty value", "region", "", true},
		{"prefixed key", "kubernetes.io/arch", "amd64", true},
		{"name of 63", name63, "x", true},
		{"name of 64", name63 + "a", "x", false},
		{"value of 63", "k", name63, true},
		{"value of 64", "k", name63 + "a", false},
		{"prefix of 253", prefix253 + "/n", "v", true},
		{"prefix of 254", prefix253 + "x/n", "v", false},
		{"dots in name and value", "a.b_c-d", "x.y_z-w", true},
		{"empty key", "", "v", false},
		{"empty name after prefix", "example.com/", "v", false},
		{"space in key", "bad key", "v", false},
		{"space in value", "k", "bad value", false},
		{"slash in value", "k", "a/b", false},
		{"two slashes in key", "a.com/b/c", "v", false},
		{"key starts with dash", "-k", "v", false},
		{"key ends with dot", "k.", "v", false},
		{"value starts with underscore", "k", "_v", false},
		{"uppercase prefix", "Example.com/n", "v", false},
		{"underscore in prefix", "ex_ample.com/n", "v", false},
		{"non-ASCII key", "ké", "v", false},
		{"expression text", "${{ matrix.os }}", "v", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidNodeLabel(tt.k, tt.v); got != tt.want {
				t.Errorf("ValidNodeLabel(%q, %q) = %v, want %v", tt.k, tt.v, got, tt.want)
			}
		})
	}
}
