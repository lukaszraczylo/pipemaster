package provider

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// Registry holds providers by ID.
type Registry struct {
	byID map[string]Provider
	mu   sync.RWMutex
}

// NewRegistry returns an empty registry; tests use it to avoid touching the default.
func NewRegistry() *Registry { return &Registry{byID: map[string]Provider{}} }

var defaultRegistry = NewRegistry()

// Register adds p and panics on a duplicate ID, which is an init-time programming error.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byID[p.ID()]; dup {
		panic(fmt.Sprintf("provider %q registered twice", p.ID()))
	}
	r.byID[p.ID()] = p
}

// Get returns the provider with the given ID.
func (r *Registry) Get(id string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (known: %s)", id, strings.Join(r.idsLocked(), ", "))
	}
	return p, nil
}

// IDs returns the registered IDs, sorted.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.idsLocked()
}

func (r *Registry) idsLocked() []string {
	ids := make([]string, 0, len(r.byID))
	for id := range r.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Detect returns the sorted IDs of every provider whose config exists in fsys.
func (r *Registry) Detect(fsys fs.FS) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for _, id := range r.idsLocked() {
		if r.byID[id].Detect(fsys) {
			out = append(out, id)
		}
	}
	return out
}

// Register adds p to the default registry.
func Register(p Provider) { defaultRegistry.Register(p) }

// Get looks id up in the default registry.
func Get(id string) (Provider, error) { return defaultRegistry.Get(id) }

// IDs lists the default registry's IDs.
func IDs() []string { return defaultRegistry.IDs() }

// Detect runs detection over the default registry.
func Detect(fsys fs.FS) []string { return defaultRegistry.Detect(fsys) }
