package argo_test

import (
	"testing"
)

func TestRemovedMutexDoesNotRestoreTheCancelFlag(t *testing.T) {
	pl, rep := editedMarked(t, "  synchronization:\n    mutexes:\n      - name: ci\n", "")
	if pl.Concurrency != nil {
		t.Errorf("concurrency %+v came back although the mutex was removed", pl.Concurrency)
	}
	wantIgnored(t, rep)
}
