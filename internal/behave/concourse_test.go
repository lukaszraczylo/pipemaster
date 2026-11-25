//go:build behave

package behave

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const smokePipeline = `resources:
  - name: source
    type: git
    source:
      uri: ((git-uri))
      branch: main
jobs:
  - name: smoke
    plan:
      - get: source
        trigger: true
      - task: say
        config:
          platform: linux
          image_resource:
            type: registry-image
            source: {repository: alpine, tag: "3.20"}
          run:
            path: sh
            args: [-ec, 'echo "MARK smoke"']
`

// TestBehaveConcourseSmoke proves the Concourse harness with a hand-written pipeline, so a failure on a converted
// pipeline cannot come from the containers, the git daemon or the fly calls.
func TestBehaveConcourseSmoke(t *testing.T) {
	if os.Getenv(envOnly) != "" || os.Getenv(envTargets) != "" {
		t.Skip("a filtered run skips the Concourse smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	work := os.Getenv(envWork)
	if work == "" {
		work = t.TempDir()
	}
	env, err := NewEnv(ctx, work)
	if err != nil {
		t.Fatalf("environment: %v", err)
	}
	if err := env.RequireKiriContext(ctx); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(work, "smoke-src")
	if err := os.MkdirAll(src, 0o750); err != nil { // #nosec G703 -- a path under the run work dir
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pipeline.yml"), []byte(smokePipeline), 0o600); err != nil { // #nosec G703 -- a path under the run work dir
		t.Fatal(err)
	}
	w, err := env.NewWorkspace(ctx, src, filepath.Join(work, "smoke"), events[EvPushMain])
	if err != nil {
		t.Fatal(err)
	}
	conc := NewConcourse(env)
	defer conc.Stop(context.Background())
	tr, err := (&concourseEngine{env: env, conc: conc}).runFile(ctx, w, filepath.Join(w.Dir, "pipeline.yml"))
	if err != nil {
		t.Fatalf("smoke pipeline: %v", err)
	}
	if !slices.Contains(tr.Markers, "smoke") || tr.Exit != 0 {
		t.Fatalf("smoke pipeline: markers %q, exit %d, log %s", tr.Markers, tr.Exit, tail(tr.Log, 600))
	}
}
