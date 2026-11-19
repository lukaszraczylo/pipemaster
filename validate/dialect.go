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
	Bools    []string
}

var (
	dialectMu sync.RWMutex
	dialects  = map[string]DialectRules{}
)

// permissionLevels are the values the GHA schema accepts for one permission scope.
var permissionLevels = []string{"read", "write", "none"}

// rulesFor returns the rules registered for id.
func rulesFor(id string) (DialectRules, bool) {
	dialectMu.RLock()
	defer dialectMu.RUnlock()
	r, ok := dialects[id]
	return r, ok
}

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
	if err := stripScopes(wf, r.Scopes, true); err != nil {
		return nil, err
	}
	if err := dropKeys(wf, r.Workflow, r.Bools); err != nil {
		return nil, err
	}
	jobs, _ := wf["jobs"].(map[string]any)
	for id, raw := range jobs {
		job, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if err := stripScopes(job, r.Scopes, false); err != nil {
			return nil, fmt.Errorf("job %s: %w", id, err)
		}
		if err := dropKeys(job, r.Job, r.Bools); err != nil {
			return nil, fmt.Errorf("job %s: %w", id, err)
		}
		steps, _ := job["steps"].([]any)
		for _, s := range steps {
			if step, ok := s.(map[string]any); ok {
				if err := dropKeys(step, r.Step, r.Bools); err != nil {
					return nil, fmt.Errorf("job %s: %w", id, err)
				}
			}
		}
	}
	return wf, nil
}

// dropKeys removes the dialect-only keys from m once each key in bools holds a boolean.
func dropKeys(m map[string]any, keys, bools []string) error {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if _, isBool := v.(bool); slices.Contains(bools, k) && !isBool {
			return fmt.Errorf("key %q must be a boolean, got %v", k, v)
		}
		delete(m, k)
	}
	return nil
}

// stripScopes removes the scopes of m's permissions map that GHA lacks and the dialect accepts. With strict, a
// scope outside the dialect's list is an error even when GHA knows it. Only the workflow-level map is strict:
// the emitter filters it, while a job-level map is Ext the emitter passes through unchanged.
func stripScopes(m map[string]any, dialect []string, strict bool) error {
	perms, ok := m["permissions"].(map[string]any)
	if !ok {
		return nil
	}
	gha := ghaScopes()
	for k := range perms {
		if slices.Contains(gha, k) {
			if strict && !slices.Contains(dialect, k) {
				return fmt.Errorf("permission scope %q is not accepted by the dialect", k)
			}
			continue
		}
		if !slices.Contains(dialect, k) {
			return fmt.Errorf("permission scope %q is not accepted by the dialect", k)
		}
		if lvl, _ := perms[k].(string); !slices.Contains(permissionLevels, lvl) {
			return fmt.Errorf("permission scope %q must be one of %v, got %v", k, permissionLevels, perms[k])
		}
		delete(perms, k)
	}
	return nil
}
