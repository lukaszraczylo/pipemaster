// Package woodpecker parses Woodpecker CI 3.x workflows into the IR.
package woodpecker

import (
	"errors"
	"io/fs"
	"strings"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

const (
	dialect    = provider.Woodpecker
	pipelineID = "woodpecker"
	dirName    = ".woodpecker"
	fileYAML   = ".woodpecker.yaml"
	fileYML    = ".woodpecker.yml"
)

type adapter struct{}

// New returns the Woodpecker CI provider.
func New() provider.Provider { return adapter{} }

func init() {
	provider.Register(New())
	validate.Register(dialect, schemas.Woodpecker, []string{fileYAML, fileYML, dirName + "/*.yaml", dirName + "/*.yml"})
}

func (adapter) ID() string { return dialect }

// Detect reports whether the repo holds a Woodpecker workflow.
func (a adapter) Detect(fsys fs.FS) bool {
	d, err := discover(fsys)
	return err == nil && len(d.files) > 0
}

// SourceFiles lists the files Parse reads.
func (adapter) SourceFiles(fsys fs.FS) ([]string, error) {
	d, err := discover(fsys)
	return d.files, err
}

// found is the files Parse reads and the other forms that are present and ignored.
type found struct {
	files   []string
	ignored []string
}

func isYAMLName(n string) bool { return strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") }

func regular(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}

// discover applies Woodpecker's order: the .woodpecker directory (its YAML files, no subfolders), then
// .woodpecker.yaml, then .woodpecker.yml. The first form found wins and nothing merges.
func discover(fsys fs.FS) (found, error) {
	var d found
	ents, err := fs.ReadDir(fsys, dirName)
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrInvalid) {
		return d, err
	}
	for _, e := range ents {
		if !e.IsDir() && isYAMLName(e.Name()) {
			d.files = append(d.files, dirName+"/"+e.Name())
		}
	}
	forms := []string{fileYAML, fileYML}
	if len(d.files) == 0 {
		d.files, forms = nil, forms[:0]
		for _, f := range []string{fileYAML, fileYML} {
			switch {
			case !regular(fsys, f):
			case d.files == nil:
				d.files = []string{f}
			default:
				forms = append(forms, f)
			}
		}
	}
	for _, f := range forms {
		if regular(fsys, f) {
			d.ignored = append(d.ignored, f)
		}
	}
	return d, nil
}
