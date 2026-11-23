package cli

import "testing"

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name, linked, module, want string
	}{
		{"dev build from a checkout", "dev", "(devel)", "dev"},
		{"dev build with no module version", "dev", "", "dev"},
		{"go install of a tagged module", "dev", "v1.2.3", "v1.2.3"},
		{"release build wins over the module version", "1.0.0", "v1.2.3", "1.0.0"},
		{"release build with a checkout module version", "1.0.0", "(devel)", "1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.linked, tt.module); got != tt.want {
				t.Errorf("resolveVersion(%q, %q) = %q, want %q", tt.linked, tt.module, got, tt.want)
			}
		})
	}
}
