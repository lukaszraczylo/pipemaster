package validate

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
)

// DialectRules are the keys and permission scopes a GHA-family dialect adds to the GHA schema.
type DialectRules struct {
	Workflow []string
	Job      []string
	Step     []string
	Scopes   []string
}

var (
	dialectMu sync.RWMutex
	dialects  = map[string]DialectRules{}
)

// RegisterDialect records the rules File applies, before the GHA schema, to a dialect's files.
func RegisterDialect(id string, r DialectRules) {
	dialectMu.Lock()
	defer dialectMu.Unlock()
	dialects[id] = r
}

// PermissionScopes lists the scope names the GHA schema accepts under permissions, sorted.
func PermissionScopes() []string { return slices.Clone(ghaScopes()) }

var ghaScopes = sync.OnceValue(func() []string {
	raw, err := schemaFS.ReadFile(ghaEntry.file)
	if err != nil {
		panic("validate: embedded GHA schema missing: " + err.Error())
	}
	var doc struct {
		Definitions map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic("validate: embedded GHA schema unreadable: " + err.Error())
	}
	var out []string
	for k := range doc.Definitions["permissions-event"].Properties {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
})

// stripDialect removes the dialect's own keys and scopes from a workflow document so the GHA schema can
// check the rest. A permission scope that neither GHA nor the dialect accepts is an error.
func stripDialect(doc any, r DialectRules) (any, error) {
	wf, ok := doc.(map[string]any)
	if !ok {
		return doc, nil
	}
	if err := stripScopes(wf, r.Scopes); err != nil {
		return nil, err
	}
	dropKeys(wf, r.Workflow)
	jobs, _ := wf["jobs"].(map[string]any)
	for id, raw := range jobs {
		job, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if err := stripScopes(job, r.Scopes); err != nil {
			return nil, fmt.Errorf("job %s: %w", id, err)
		}
		dropKeys(job, r.Job)
		steps, _ := job["steps"].([]any)
		for _, s := range steps {
			if step, ok := s.(map[string]any); ok {
				dropKeys(step, r.Step)
			}
		}
	}
	return wf, nil
}

func dropKeys(m map[string]any, keys []string) {
	for _, k := range keys {
		delete(m, k)
	}
}

// stripScopes removes the scopes of m's permissions map that GHA lacks and the dialect accepts.
func stripScopes(m map[string]any, dialect []string) error {
	perms, ok := m["permissions"].(map[string]any)
	if !ok {
		return nil
	}
	gha := ghaScopes()
	for k := range perms {
		if slices.Contains(gha, k) {
			continue
		}
		if !slices.Contains(dialect, k) {
			return fmt.Errorf("permission scope %q is not accepted by the dialect", k)
		}
		delete(perms, k)
	}
	return nil
}
