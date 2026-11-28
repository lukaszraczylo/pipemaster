package nsguard_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/lukaszraczylo/pipemaster/internal/behave/nsguard"
)

// cluster is a fake API: it records the calls and fails the first failDelete deletes or the first create.
type cluster struct {
	created    []string
	deleted    []string
	failDelete int
	mu         sync.Mutex
	failCreate bool
}

func (c *cluster) create(_ context.Context, ns string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failCreate {
		return errors.New("create refused")
	}
	c.created = append(c.created, ns)
	return nil
}

func (c *cluster) del(_ context.Context, ns string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, ns)
	if c.failDelete > 0 {
		c.failDelete--
		return errors.New("delete refused")
	}
	return nil
}

func TestCleanupDeletesAndSweepSkipsIt(t *testing.T) {
	c := &cluster{}
	g := nsguard.New("pm", c.create, c.del)
	_, cleanup, err := g.Namespace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if err := g.Sweep(); err != nil {
		t.Fatal(err)
	}
	if len(c.deleted) != 1 || len(g.Open()) != 0 {
		t.Fatalf("deleted %v, still tracked %v", c.deleted, g.Open())
	}
}

func TestCleanupRetriesAFailedDelete(t *testing.T) {
	c := &cluster{failDelete: 2}
	g := nsguard.New("pm", c.create, c.del)
	_, cleanup, _ := g.Namespace(context.Background())
	cleanup()
	if len(c.deleted) != 3 || len(g.Open()) != 0 {
		t.Fatalf("delete attempts %d, tracked %v, want 3 attempts and none tracked", len(c.deleted), g.Open())
	}
}

func TestSweepDeletesWhatCleanupMissedAndReportsAFinalFailure(t *testing.T) {
	c := &cluster{failDelete: nsguard.DeleteTries}
	g := nsguard.New("pm", c.create, c.del)
	_, cleanup, _ := g.Namespace(context.Background())
	cleanup()
	if len(g.Open()) != 1 {
		t.Fatalf("a failed delete must leave the namespace tracked: %v", g.Open())
	}
	if err := g.Sweep(); err != nil {
		t.Fatalf("the sweep delete succeeds: %v", err)
	}
	if len(g.Open()) != 0 || len(c.deleted) != nsguard.DeleteTries+1 {
		t.Fatalf("tracked %v, delete attempts %d", g.Open(), len(c.deleted))
	}

	stuck := &cluster{failDelete: 100}
	g = nsguard.New("pm", stuck.create, stuck.del)
	_, _, _ = g.Namespace(context.Background())
	err := g.Sweep()
	if err == nil {
		t.Fatal("a namespace that cannot be deleted must be returned, not swallowed")
	}
	if len(g.Open()) != 1 {
		t.Fatalf("the namespace stays tracked: %v", g.Open())
	}
}

func TestFailedCreateStillDeletes(t *testing.T) {
	c := &cluster{failCreate: true}
	g := nsguard.New("pm", c.create, c.del)
	if _, _, err := g.Namespace(context.Background()); err == nil {
		t.Fatal("want the create error")
	}
	if len(c.deleted) != 1 || len(g.Open()) != 0 {
		t.Fatalf("a create that failed may have reached the server: deleted %v, tracked %v", c.deleted, g.Open())
	}
}

func TestCleanupRunsOnPanic(t *testing.T) {
	c := &cluster{}
	g := nsguard.New("pm", c.create, c.del)
	func() {
		defer func() { _ = recover() }()
		_, cleanup, err := g.Namespace(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		panic("engine panic")
	}()
	if len(c.deleted) != 1 {
		t.Fatalf("deletes after a panic = %d, want 1", len(c.deleted))
	}
}

func TestNamesAreUnique(t *testing.T) {
	c := &cluster{}
	g := nsguard.New("pm", c.create, c.del)
	a, _, _ := g.Namespace(context.Background())
	b, _, _ := g.Namespace(context.Background())
	if a == b {
		t.Fatalf("two namespaces share the name %s", a)
	}
}
