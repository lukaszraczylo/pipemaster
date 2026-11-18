// Package gha parses GitHub Actions workflows, and dialect forks of them, into the IR.
package gha

import (
	"io/fs"
	"path"
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider"
)

// Dialect is the delta table that distinguishes one GitHub Actions family member from another.
// ID names the provider and the Ext key; Ignored lists the workflow keys the dialect's runner ignores.
type Dialect struct {
	ID      string
	Dir     string
	Ignored []string
}

// GHA is GitHub Actions itself, the base dialect.
var GHA = Dialect{ID: provider.GHA, Dir: ".github/workflows"}

type adapter struct{ d Dialect }

// New returns the provider for dialect d.
func New(d Dialect) provider.Provider { return &adapter{d: d} }

func init() { provider.Register(New(GHA)) }

func (a *adapter) ID() string { return a.d.ID }

// Detect reports whether the dialect's workflow directory holds at least one workflow file.
func (a *adapter) Detect(fsys fs.FS) bool {
	files, err := workflowFiles(fsys, a.d.Dir)
	return err == nil && len(files) > 0
}

// workflowFiles lists the .yml and .yaml files directly inside dir, sorted by name.
func workflowFiles(fsys fs.FS, dir string) ([]string, error) {
	ents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		ext := strings.ToLower(path.Ext(e.Name()))
		if !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
			out = append(out, path.Join(dir, e.Name()))
		}
	}
	return out, nil
}
