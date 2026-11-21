// Package tekton parses Tekton Pipelines and Triggers files under .tekton/ into the IR.
package tekton

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/report"
)

const (
	dialect = provider.Tekton
	dirName = ".tekton"

	apiPipelines = "tekton.dev/"
	apiTriggers  = "triggers.tekton.dev/"
	apiBatch     = "batch/"

	kindPipeline        = "Pipeline"
	kindPipelineRun     = "PipelineRun"
	kindTask            = "Task"
	kindEventListener   = "EventListener"
	kindTrigger         = "Trigger"
	kindTriggerTemplate = "TriggerTemplate"
	kindCronJob         = "CronJob"
)

// ErrNoEmitter is returned by Emit until the Tekton emitter exists.
var ErrNoEmitter = errors.New("tekton: the emitter is not available yet")

type adapter struct{}

// New returns the Tekton provider.
func New() provider.Provider { return adapter{} }

func init() { provider.Register(New()) }

func (adapter) ID() string { return dialect }

// Caps declares nothing until the emitter exists; the parser needs no capability matrix.
func (adapter) Caps() caps.Matrix { return caps.Matrix{} }

// Emit is not available yet.
func (adapter) Emit(*ir.Project) (map[string][]byte, *report.Report, error) {
	return nil, nil, ErrNoEmitter
}

// SourceFiles lists the YAML files under .tekton/ that Parse reads, in path order.
func (adapter) SourceFiles(fsys fs.FS) ([]string, error) { return yamlFiles(fsys) }

// Detect reports whether a file under .tekton/ holds a Pipeline or a PipelineRun of a tekton.dev API.
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
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		for {
			var head struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
			}
			if dec.Decode(&head) != nil {
				break
			}
			if strings.HasPrefix(head.APIVersion, apiPipelines) && (head.Kind == kindPipeline || head.Kind == kindPipelineRun) {
				return true
			}
		}
	}
	return false
}

// yamlFiles lists the regular .yaml and .yml files under .tekton/, recursively. A repo without the directory has none.
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
