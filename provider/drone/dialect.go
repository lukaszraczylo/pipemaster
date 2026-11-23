// Package drone parses Drone CI pipelines (.drone.yml) into the IR.
package drone

import (
	"io/fs"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

const (
	dialect    = provider.Drone
	pipelineID = "drone"
	fileYML    = ".drone.yml"
	// kubernetesDefsURL is where drone.json refers to the Kubernetes definitions, resolved against its $id.
	kubernetesDefsURL = "https://json.schemastore.org/kubernetes-definitions.json"
)

// scripted lists the generator files that Drone reads when a repo setting names them. The parser reads none.
var scripted = []string{".drone.jsonnet", ".drone.star"}

type adapter struct{}

// New returns the Drone CI provider.
func New() provider.Provider { return adapter{} }

func init() {
	provider.Register(New())
	validate.RegisterLazyResource(kubernetesDefsURL, schemas.KubernetesDefinitions)
	validate.Register(dialect, schemas.Drone, []string{fileYML})
}

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

func regular(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}
