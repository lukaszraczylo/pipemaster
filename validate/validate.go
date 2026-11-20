// Package validate checks emitted files against pinned provider JSON Schemas.
package validate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// ErrNoSchema means no schema is pinned for the provider.
var ErrNoSchema = errors.New("no schema for provider")

type entry struct {
	err    error
	schema *jsonschema.Schema
	id     string
	raw    []byte
	once   sync.Once
}

var (
	regMu      sync.RWMutex
	pinned     = map[string]*entry{}
	patterns   = map[string][]string{}
	sharedByID = map[[sha256.Size]byte]*entry{}
)

// Register pins schema (JSON) for provider id and the fs.Glob patterns of its files. Providers with
// identical schema bytes share one compiled schema. It panics on a duplicate id or an unparsable schema.
func Register(id string, schema []byte, patternList []string) {
	var head struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(schema, &head); err != nil || head.ID == "" {
		panic(fmt.Sprintf("validate: schema for %q has no readable $id: %v", id, err))
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := pinned[id]; dup {
		panic("validate: duplicate schema registration for " + id)
	}
	sum := sha256.Sum256(schema)
	e, ok := sharedByID[sum]
	if !ok {
		e = &entry{id: head.ID, raw: schema}
		sharedByID[sum] = e
	}
	pinned[id] = e
	patterns[id] = append([]string(nil), patternList...)
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
	regMu.RLock()
	e, ok := pinned[providerID]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrNoSchema, providerID)
	}
	e.once.Do(func() {
		e.schema, e.err = compileDoc(e.id, e.raw)
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
	docs, err := toJSONValues(data)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	for _, v := range docs {
		if rules, ok := rulesFor(providerID); ok {
			var err error
			if v, err = stripDialect(v, rules); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		if err := validateDoc(sch, v); err != nil {
			return fmt.Errorf("%s: invalid for %s schema: %w", name, providerID, err)
		}
	}
	return nil
}

// validateDoc checks one decoded YAML document against sch.
func validateDoc(sch *jsonschema.Schema, v any) error {
	// Round-trip so numbers take the json.Number form the library requires.
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return err
	}
	return sch.Validate(inst)
}

// toJSONValues decodes every document of a multi-document YAML stream.
func toJSONValues(data []byte) ([]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []any
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse yaml: %w", err)
		}
		n, err := normalize(v)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return toJSONValues([]byte("null\n"))
	}
	return out, nil
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

// Patterns returns the fs.Glob patterns of the files validated as providerID, or nil when none are pinned.
func Patterns(providerID string) []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return append([]string(nil), patterns[providerID]...)
}

// Providers lists the provider IDs that have a pinned schema, sorted.
func Providers() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	ids := make([]string, 0, len(pinned))
	for id := range pinned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
