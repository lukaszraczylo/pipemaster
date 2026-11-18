// Package validate checks emitted files against pinned provider JSON Schemas.
package validate

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/lukaszraczylo/pipemaster/provider"
)

//go:embed schemas/*.json
var schemaFS embed.FS

// ErrNoSchema means no schema is pinned for the provider.
var ErrNoSchema = errors.New("no schema for provider")

type schemaSpec struct {
	file string
	id   string
}

var specs = map[string]schemaSpec{
	provider.GHA:    {file: "schemas/github-workflow.json", id: "https://json.schemastore.org/github-workflow.json"},
	provider.GitLab: {file: "schemas/gitlab-ci.json", id: "https://gitlab.com/.gitlab-ci.yml"},
}

type compiled struct {
	err    error
	schema *jsonschema.Schema
	once   sync.Once
}

var cache = map[string]*compiled{
	provider.GHA:    {},
	provider.GitLab: {},
}

// offlineLoader makes any reference outside the embedded schema an error.
type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("network access refused for %s", url)
}

func compile(id string) (*jsonschema.Schema, error) {
	c := cache[id]
	c.once.Do(func() {
		sp := specs[id]
		raw, err := schemaFS.ReadFile(sp.file)
		if err != nil {
			c.err = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			c.err = fmt.Errorf("parse %s: %w", sp.file, err)
			return
		}
		comp := jsonschema.NewCompiler()
		comp.UseLoader(offlineLoader{})
		if err := comp.AddResource(sp.id, doc); err != nil {
			c.err = err
			return
		}
		c.schema, c.err = comp.Compile(sp.id)
	})
	return c.schema, c.err
}

// File validates YAML data emitted for providerID against its pinned schema.
func File(providerID, name string, data []byte) error {
	if _, ok := specs[providerID]; !ok {
		return fmt.Errorf("%w %q", ErrNoSchema, providerID)
	}
	sch, err := compile(providerID)
	if err != nil {
		return fmt.Errorf("compile %s schema: %w", providerID, err)
	}
	v, err := toJSONValue(data)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	// Round-trip so numbers take the json.Number form the library requires.
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("%s: invalid for %s schema: %w", name, providerID, err)
	}
	return nil
}

func toJSONValue(data []byte) (any, error) {
	var v any
	if err := yaml.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	return normalize(v)
}

func normalize(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			ks, err := keyString(k)
			if err != nil {
				return nil, err
			}
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[ks] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	}
	return v, nil
}

func keyString(k any) (string, error) {
	switch t := k.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case uint64:
		return strconv.FormatUint(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	}
	return "", fmt.Errorf("unsupported yaml map key type %T", k)
}
