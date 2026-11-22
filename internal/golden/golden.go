// Package golden compares test output with checked-in golden files.
package golden

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Assert fails t when got differs from the file at path; -update rewrites it.
func Assert(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("golden: mkdir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatalf("golden: write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden file %s missing; run with -update to create it", path)
		}
		t.Fatalf("golden: read %s: %v", path, err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("golden mismatch for %s (-want +got):\n%s", path, diff)
	}
}

// Orphans fails t for each file under dir that is not in keep (paths relative to dir); -update deletes it.
func Orphans(t *testing.T, dir string, keep map[string]bool) {
	t.Helper()
	var stale []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err == nil && !keep[filepath.ToSlash(rel)] {
			stale = append(stale, p)
		}
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("golden: walk %s: %v", dir, err)
	}
	for _, p := range stale {
		if !*update {
			t.Errorf("golden %s is not written by the emitter; run with -update to delete it", p)
			continue
		}
		if err := os.Remove(p); err != nil {
			t.Fatalf("golden: remove %s: %v", p, err)
		}
	}
}
