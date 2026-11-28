//go:build behave

package behave

import (
	"io/fs"
	"path/filepath"
	"testing"
)

// The workspace of a run omits fixture.json. A native GHA fixture with a data file and a job with no checkout step
// would fail the checkout check on its source run, so no fixture may combine the two. The runner also applies the
// check to converted files only (Workspace.Converted).
func TestNativeGHAFixturesHaveNoDataFileWithoutCheckout(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "testdata", "behave", "gha", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no native gha fixtures found: %v", err)
	}
	for _, dir := range dirs {
		var data []string
		walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			if d.IsDir() && rel == ".github" {
				return fs.SkipDir
			}
			if d.Type().IsRegular() && rel != "fixture.json" {
				data = append(data, rel)
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("%s: %v", dir, walkErr)
		}
		if len(data) == 0 {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(dir, ".github", "workflows", "*.yml"))
		for _, f := range files {
			if why, err := missingCheckout(dir, []string{f}); err != nil || why != "" {
				t.Errorf("%s holds %v and %s (err %v)", filepath.Base(dir), data, why, err)
			}
		}
	}
}
