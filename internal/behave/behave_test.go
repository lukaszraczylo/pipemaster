//go:build behave

package behave

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	envOnly    = "BEHAVE_ONLY"
	envTargets = "BEHAVE_TARGETS"
	envOut     = "BEHAVE_OUT"
	envWork    = "BEHAVE_WORK"
)

// TestBehave runs every fixture on its source engine and every converted pipeline on its target engine. It
// fails on any difference that the loss report does not explain.
func TestBehave(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	work := os.Getenv(envWork)
	if work == "" {
		work = t.TempDir()
	}
	env, err := NewEnv(ctx, work)
	if err != nil {
		t.Fatalf("environment: %v", err)
	}
	fixtures, err := LoadFixtures(env.Root)
	if err != nil {
		t.Fatalf("fixtures: %v", err)
	}
	opts := Options{Log: t.Logf}
	filtered := false
	if v := os.Getenv(envOnly); v != "" {
		opts.Only, filtered = regexp.MustCompile(v), true
	}
	if v := os.Getenv(envTargets); v != "" {
		opts.Targets, filtered = strings.Split(v, ","), true
	}
	r := NewRunner(env, opts)
	defer r.Close()
	results, err := r.Run(ctx, fixtures)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	md := RenderMarkdown(results, r.Times())
	out := os.Getenv(envOut)
	if out == "" {
		out = filepath.Join(env.Root, "testdata", "behave", "RESULTS.md")
		if filtered {
			out = filepath.Join(work, "RESULTS.md")
		}
	}
	if err := os.WriteFile(out, []byte(md), 0o600); err != nil { // #nosec G703 -- the results path is fixed by the harness or set by the operator
		t.Fatalf("write %s: %v", out, err)
	}
	c := Counts(results)
	t.Logf("results written to %s: pass %d, diff %d, FAIL %d, blocked %d", out, c[StatusPass], c[StatusExplained], c[StatusFail], c[StatusBlocked])
	if fails := Failures(results); len(fails) > 0 {
		for _, f := range fails {
			t.Errorf("unexplained difference: %s -> %s %s %s: %s", f.Source, f.Target, f.Fixture, f.Event, f.Detail)
		}
	}
}
