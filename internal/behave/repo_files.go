//go:build behave

package behave

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// pipelineRoots are the top-level paths that hold the pipeline files of a provider. The converter writes the
// target's own pipeline files, so these never come over from the source.
var pipelineRoots = []string{".github", ".gitlab-ci.yml", ".drone.yml", ".woodpecker", ".concourse", ".tekton", ".argo", fixtureFile}

// copyRepoFiles gives the converted workspace the other files of the fixture repository, such as data that a script
// reads. A real repository holds them whatever the target is, and a missing checkout shows only when they matter.
// A file that the converter already wrote stays as it is.
func copyRepoFiles(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error { // #nosec G703 -- walks the harness own testdata tree
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil || rel == "." {
			return err
		}
		top, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if slices.Contains(pipelineRoots, top) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750) // #nosec G703 -- target is under the run work dir
		}
		if !d.Type().IsRegular() || exists(dst, rel) {
			return nil
		}
		raw, err := os.ReadFile(p) // #nosec G304 G122 -- walked inside the harness own testdata tree
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o600) // #nosec G703 -- target is under the run work dir
	})
}

const cloneHostURL = "https://github.com/"

// clonesGitHub is true when a converted Tekton or Argo document clones a repository from GitHub. The Kiri cluster
// has no git host for the fixture repository, so such a run cannot start.
func clonesGitHub(docs []map[string]any) bool {
	raw, err := json.Marshal(docs)
	return err == nil && strings.Contains(string(raw), cloneHostURL)
}
