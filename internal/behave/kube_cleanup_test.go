//go:build behave

package behave

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeKubectl puts a kubectl on PATH that logs each call. The first failTimes calls of `<failVerb> namespace` exit 1.
func fakeKubectl(t *testing.T, failVerb string, failTimes int) (*Env, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	count := filepath.Join(dir, "fails")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\n" +
		"case \"$*\" in *\" " + failVerb + " namespace\"*)\n" +
		"  n=$(cat " + count + " 2>/dev/null || echo 0)\n" +
		"  if [ \"$n\" -lt " + strconv.Itoa(failTimes) + " ]; then echo $((n+1)) > " + count + "; exit 1; fi;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o700); err != nil { // #nosec G306 G703 -- a test script in a temp dir
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &Env{Kubeconfig: filepath.Join(dir, "kubeconfig")}, log
}

func countDeletes(t *testing.T, log string) int {
	t.Helper()
	b, err := os.ReadFile(log) // #nosec G304 -- a log in a test temp dir
	if err != nil {
		return 0
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " delete namespace ") {
			n++
		}
	}
	return n
}

func TestNamespaceCleanupDeletes(t *testing.T) {
	env, log := fakeKubectl(t, "none", 0)
	k := NewKube(env)
	ns, cleanup, err := k.Namespace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := countDeletes(t, log); got != 1 {
		t.Fatalf("deletes of %s = %d, want 1", ns, got)
	}
	k.sweep()
	if got := countDeletes(t, log); got != 1 {
		t.Fatalf("sweep deleted a clean namespace again: %d deletes", got)
	}
}

func TestNamespaceCleanupRetriesAFailedDelete(t *testing.T) {
	env, log := fakeKubectl(t, "delete", 2)
	k := NewKube(env)
	_, cleanup, err := k.Namespace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := countDeletes(t, log); got != 3 {
		t.Fatalf("delete attempts = %d, want 3 (2 failures, 1 success)", got)
	}
	if len(k.open) != 0 {
		t.Fatalf("namespace still tracked after a successful delete: %v", k.open)
	}
}

func TestSweepDeletesWhatCleanupMissed(t *testing.T) {
	env, log := fakeKubectl(t, "delete", 3)
	k := NewKube(env)
	_, cleanup, err := k.Namespace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanup() // all three attempts fail: the namespace stays tracked
	if len(k.open) != 1 {
		t.Fatalf("a failed delete must leave the namespace tracked: %v", k.open)
	}
	k.sweep()
	if len(k.open) != 0 {
		t.Fatalf("sweep left %v", k.open)
	}
	if got := countDeletes(t, log); got != 4 {
		t.Fatalf("delete attempts = %d, want 4", got)
	}
}

func TestFailedCreateStillDeletes(t *testing.T) {
	env, log := fakeKubectl(t, "create", 1)
	k := NewKube(env)
	if _, _, err := k.Namespace(context.Background()); err == nil {
		t.Fatal("want the create error")
	}
	if got := countDeletes(t, log); got != 1 {
		t.Fatalf("a create that failed may have reached the server; deletes = %d, want 1", got)
	}
}

func TestCleanupRunsOnPanic(t *testing.T) {
	env, log := fakeKubectl(t, "none", 0)
	k := NewKube(env)
	func() {
		defer func() { _ = recover() }()
		_, cleanup, err := k.Namespace(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		panic("engine panic")
	}()
	if got := countDeletes(t, log); got != 1 {
		t.Fatalf("deletes after a panic = %d, want 1", got)
	}
}
