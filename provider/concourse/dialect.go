package concourse

import (
	"io/fs"
	"path"
	"sort"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
	"github.com/lukaszraczylo/pipemaster/validate/schemas"
)

const (
	dialect = provider.Concourse
	dirName = ".concourse"

	keyJobs = "jobs"
	keyPlan = "plan"
)

type adapter struct{}

// New returns the Concourse provider.
func New() provider.Provider { return adapter{} }

func init() {
	provider.Register(New())
	validate.Register(dialect, schemas.Concourse, []string{dirName + "/*.yml", dirName + "/*.yaml"})
}

func (adapter) ID() string { return dialect }

// SourceFiles lists the YAML files under .concourse/ that Parse reads, in path order.
func (adapter) SourceFiles(fsys fs.FS) ([]string, error) { return yamlFiles(fsys) }

// Detect reports whether a file under .concourse/ has a jobs list with a plan. It reads with the plain YAML
// decoder, so a file with a repeated key is still detected and Parse then names the error.
func (adapter) Detect(fsys fs.FS) bool {
	files, err := yamlFiles(fsys)
	if err != nil {
		return false
	}
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			continue
		}
		var doc yaml.Node
		if yaml.Unmarshal(data, &doc) == nil && hasPlanJob(&doc) {
			return true
		}
	}
	return false
}

func hasPlanJob(doc *yaml.Node) bool {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != keyJobs || top.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, j := range top.Content[i+1].Content {
			for n := j; n != nil && n.Kind == yaml.AliasNode; n = n.Alias {
				j = n.Alias
			}
			if j == nil || j.Kind != yaml.MappingNode {
				continue
			}
			for k := 0; k+1 < len(j.Content); k += 2 {
				if j.Content[k].Value == keyPlan {
					return true
				}
			}
		}
	}
	return false
}

// yamlFiles lists the regular .yml and .yaml files under .concourse/, recursively. A repo without it has none.
func yamlFiles(fsys fs.FS) ([]string, error) {
	if st, err := fs.Stat(fsys, dirName); err != nil || !st.IsDir() {
		return nil, nil
	}
	var out []string
	err := fs.WalkDir(fsys, dirName, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ext := path.Ext(p); d.Type().IsRegular() && (ext == ".yaml" || ext == ".yml") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
