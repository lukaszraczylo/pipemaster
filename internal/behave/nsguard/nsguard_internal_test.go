package nsguard

import (
	"context"
	"testing"
	"time"
)

func TestReleaseBoundsAHungDelete(t *testing.T) {
	hung := func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	g := New("t", func(context.Context, string) error { return nil }, hung)
	g.tryTimeout, g.budget = 40*time.Millisecond, 100*time.Millisecond
	g.open["ns"] = struct{}{}
	start := time.Now()
	if err := g.release("ns"); err == nil {
		t.Fatal("release of a hung delete returned no error")
	}
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Fatalf("release took %v, want at most the budget plus slack", took)
	}
	if len(g.Open()) != 1 {
		t.Fatal("a namespace whose delete hung must stay tracked for the sweep")
	}
}
