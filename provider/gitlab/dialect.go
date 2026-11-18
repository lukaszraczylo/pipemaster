// Package gitlab parses GitLab CI pipelines into the IR.
package gitlab

import (
	"errors"
	"io/fs"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	dialect    = provider.GitLab
	rootFile   = ".gitlab-ci.yml"
	pipelineID = "gitlab-ci"
)

type adapter struct{}

// New returns the GitLab CI provider.
func New() provider.Provider { return adapter{} }

func init() { provider.Register(New()) }

func (adapter) ID() string { return dialect }

// Detect reports whether the repo root holds a .gitlab-ci.yml.
func (adapter) Detect(fsys fs.FS) bool {
	st, err := fs.Stat(fsys, rootFile)
	return err == nil && !st.IsDir()
}

// Emit is not available until the GitLab emitter lands.
func (adapter) Emit(*ir.Project) (map[string][]byte, *report.Report, error) {
	return nil, nil, errors.New("gitlab: emit is not implemented")
}
