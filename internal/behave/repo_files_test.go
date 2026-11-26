//go:build behave

package behave

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyRepoFiles(t *testing.T) {
	src := writeTree(t, map[string]string{
		"data/seed.txt":       "seed",
		".concourse/ci.yml":   "pipeline",
		"fixture.json":        "{}",
		"ci/child.yml":        "child",
		"keep.txt":            "from the fixture",
		".github/workflow.md": "not copied",
	})
	dst := writeTree(t, map[string]string{
		".github/workflows/ci.yml": "converted",
		"keep.txt":                 "from the converter",
	})
	if err := copyRepoFiles(src, dst); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		raw, err := os.ReadFile(filepath.Join(dst, rel)) // #nosec G304 -- a path inside a test temp dir
		if err != nil {
			return ""
		}
		return string(raw)
	}
	tests := []struct{ rel, want string }{
		{"data/seed.txt", "seed"},
		{"ci/child.yml", "child"},
		{"keep.txt", "from the converter"},
		{".concourse/ci.yml", ""},
		{"fixture.json", ""},
		{".github/workflow.md", ""},
		{".github/workflows/ci.yml", "converted"},
	}
	for _, tc := range tests {
		if got := read(tc.rel); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.rel, got, tc.want)
		}
	}
}
