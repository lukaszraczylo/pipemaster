// Package nsguard tracks the namespaces of one run and deletes every one of them, whatever happens to the run.
package nsguard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DeleteTries is how often a delete runs before the namespace stays tracked for the sweep.
const DeleteTries = 3

// deleteTimeout bounds one delete call, which runs on a context that no cancel of the run can stop.
const deleteTimeout = time.Minute

// Op creates or deletes the namespace ns on the cluster.
type Op func(ctx context.Context, ns string) error

// Guard names, tracks and deletes namespaces.
type Guard struct {
	create, del Op
	open        map[string]struct{}
	prefix      string
	counter     atomic.Int64
	stamp       int64
	mu          sync.Mutex
}

// New returns a Guard whose namespaces start with prefix.
func New(prefix string, create, del Op) *Guard {
	return &Guard{create: create, del: del, prefix: prefix, stamp: time.Now().Unix(), open: map[string]struct{}{}}
}

// Namespace creates a fresh namespace and returns it with its cleanup. The namespace is tracked before the create
// call, so a create that times out after the server accepted it is still deleted by the cleanup or by Sweep.
func (g *Guard) Namespace(ctx context.Context) (string, func(), error) {
	ns := fmt.Sprintf("%s-%d-%d", g.prefix, g.stamp, g.counter.Add(1))
	g.mu.Lock()
	g.open[ns] = struct{}{}
	g.mu.Unlock()
	if err := g.create(ctx, ns); err != nil {
		relErr := g.release(ns)
		return "", nil, errors.Join(fmt.Errorf("create namespace %s: %w", ns, err), relErr)
	}
	return ns, func() {
		if err := g.release(ns); err != nil {
			slog.Error("namespace not deleted; the sweep retries", "namespace", ns, "error", err)
		}
	}, nil
}

// release deletes one tracked namespace on a context that no cancel or timeout of the run can stop. It retries,
// because a namespace that stays behind is the failure this exists to prevent. It returns the last failure.
func (g *Guard) release(ns string) error {
	var last error
	for range DeleteTries {
		c, cancel := context.WithTimeout(context.Background(), deleteTimeout)
		last = g.del(c, ns)
		cancel()
		if last == nil {
			g.mu.Lock()
			delete(g.open, ns)
			g.mu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("delete namespace %s after %d tries: %w", ns, DeleteTries, last)
}

// Sweep deletes every namespace that a cleanup did not remove and returns the failures.
func (g *Guard) Sweep() error {
	g.mu.Lock()
	left := make([]string, 0, len(g.open))
	for ns := range g.open {
		left = append(left, ns)
	}
	g.mu.Unlock()
	sort.Strings(left)
	var errs []error
	for _, ns := range left {
		if err := g.release(ns); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Open returns the namespaces that are still tracked.
func (g *Guard) Open() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.open))
	for ns := range g.open {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}
