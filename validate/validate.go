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

type entry struct {
	err    error
	schema *jsonschema.Schema
	file   string
	id     string
	once   sync.Once
}

var schemas = map[string]*entry{
	provider.GHA:    {file: "schemas/github-workflow.json", id: "https://json.schemastore.org/github-workflow.json"},
	provider.GitLab: {file: "schemas/gitlab-ci.json", id: "https://gitlab.com/.gitlab-ci.yml"},
}

// offlineLoader makes any reference outside the embedded schema an error.
type offlineLoader struct{}

func (offlineLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("network access refused for %s", url)
}

func compileDoc(id string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", id, err)
	}
	comp := jsonschema.NewCompiler()
	comp.UseLoader(offlineLoader{})
	if err := comp.AddResource(id, doc); err != nil {
		return nil, err
	}
	return comp.Compile(id)
}

func schemaFor(providerID string) (*jsonschema.Schema, error) {
	e, ok := schemas[providerID]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrNoSchema, providerID)
	}
	e.once.Do(func() {
		raw, err := schemaFS.ReadFile(e.file)
		if err != nil {
			e.err = err
			return
		}
		e.schema, e.err = compileDoc(e.id, raw)
	})
	return e.schema, e.err
}

// File validates YAML data emitted for providerID against its pinned schema.
func File(providerID, name string, data []byte) error {
	sch, err := schemaFor(providerID)
	if err != nil {
		if errors.Is(err, ErrNoSchema) {
			return err
		}
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
