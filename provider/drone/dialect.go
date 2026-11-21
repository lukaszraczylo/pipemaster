// Package drone parses Drone CI pipelines (.drone.yml) into the IR.
package drone

import (
	"errors"
	"io/fs"

	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	dialect    = provider.Drone
	pipelineID = "drone"
	fileYML    = ".drone.yml"
)

// scripted lists the generator files that Drone reads when a repo setting names them. The parser reads none.
var scripted = []string{".drone.jsonnet", ".drone.star"}

// ErrNoEmitter is returned by Emit until the Drone emitter exists.
var ErrNoEmitter = errors.New("drone: the emitter is not available yet")

type adapter struct{}

// New returns the Drone CI provider.
func New() provider.Provider { return adapter{} }

func init() { provider.Register(New()) }

func (adapter) ID() string { return dialect }

// Detect reports whether the repo holds a .drone.yml. A generator file alone does not count.
func (adapter) Detect(fsys fs.FS) bool { return regular(fsys, fileYML) }

// SourceFiles lists the files Parse reads.
func (adapter) SourceFiles(fsys fs.FS) ([]string, error) {
	if regular(fsys, fileYML) {
		return []string{fileYML}, nil
	}
	return nil, nil
}

// Emit is not available yet.
func (adapter) Emit(*ir.Project) (map[string][]byte, *report.Report, error) {
	return nil, nil, ErrNoEmitter
}

func regular(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}
