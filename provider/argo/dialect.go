// Package argo converts Argo Workflows and Argo Events files under .argo/ to the IR.
package argo

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider"
	"github.com/lukaszraczylo/pipemaster/validate"
)

const (
	dialect = provider.Argo
	dirName = ".argo"

	apiArgo = "argoproj.io/"

	kindWorkflow     = "Workflow"
	kindTemplate     = "WorkflowTemplate"
	kindClusterTmpl  = "ClusterWorkflowTemplate"
	kindCron         = "CronWorkflow"
	kindSensor       = "Sensor"
	kindEventSource  = "EventSource"
	fallbackPipeline = "argo"
)

// helmText matches template text that only Helm renders: a trim marker, or an action that starts with a dot.
// Argo tags never start that way.
var helmText = regexp.MustCompile(`\{\{-|\{\{\s*\.[A-Za-z]`)

type adapter struct{}

// New returns the Argo provider.
func New() provider.Provider { return adapter{} }

func init() {
	provider.Register(New())
	validate.RegisterArgo(dialect, []string{dirName + "/*.yaml", dirName + "/*.yml", dirName + "/*/*.yaml", dirName + "/*/*.yml"})
}

func (adapter) ID() string { return dialect }

// SourceFiles lists the YAML files under .argo/ that Parse reads, in path order.
func (adapter) SourceFiles(fsys fs.FS) ([]string, error) { return yamlFiles(fsys) }

// isHelm tells a file that holds Helm template text, which no YAML reader can parse until it is rendered. A file
// that parses is no Helm template, even when a script in it holds Go template text such as {{.Host}}.
func isHelm(data []byte) bool {
	if !helmText.Match(data) {
		return false
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			return !errors.Is(err, io.EOF)
		}
	}
}

// Detect reports whether a file under .argo/ holds a workflow kind of an argoproj.io API. A Helm template does not count.
func (adapter) Detect(fsys fs.FS) bool {
	files, err := yamlFiles(fsys)
	if err != nil {
		return false
	}
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil || isHelm(data) {
			continue
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var head struct {
				APIVersion string `yaml:"apiVersion"`
				Kind       string `yaml:"kind"`
			}
			if dec.Decode(&head) != nil {
				break
			}
			if strings.HasPrefix(head.APIVersion, apiArgo) && isWorkflowKind(head.Kind) {
				return true
			}
		}
	}
	return false
}

func isWorkflowKind(kind string) bool {
	switch kind {
	case kindWorkflow, kindTemplate, kindClusterTmpl, kindCron:
		return true
	}
	return false
}

// yamlFiles lists the regular .yaml and .yml files under .argo/, recursively. A repo without the directory has none.
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
