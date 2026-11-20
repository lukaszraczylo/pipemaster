// Package gitlab parses GitLab CI pipelines into the IR.
package gitlab

import (
	"io/fs"
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

const (
	dialect    = provider.GitLab
	rootFile   = ".gitlab-ci.yml"
	pipelineID = "gitlab-ci"
)

type adapter struct{}

// New returns the GitLab CI provider.
func New() provider.Provider { return adapter{} }

func init() {
	provider.Register(New())
	validate.Register(dialect, schemas.GitLabCI, []string{rootFile})
}

// SourceFiles lists the root file and the other YAML files of the tree, where its includes can live.
// It lists nothing when the root file is missing.
func (adapter) SourceFiles(fsys fs.FS) ([]string, error) {
	if !(adapter{}).Detect(fsys) {
		return nil, nil
	}
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")) {
			out = append(out, p)
		}
		return err
	})
	return out, err
}

func (adapter) ID() string { return dialect }

// Detect reports whether the repo root holds a .gitlab-ci.yml.
func (adapter) Detect(fsys fs.FS) bool {
	st, err := fs.Stat(fsys, rootFile)
	return err == nil && !st.IsDir()
}
