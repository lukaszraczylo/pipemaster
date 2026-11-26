package gha_test

import (
	"strings"
	"testing"

	"github.com/lukaszraczylo/pipemaster/caps"
	"github.com/lukaszraczylo/pipemaster/ir"
	"github.com/lukaszraczylo/pipemaster/provider/gha"
)

func TestCachePolicyMapping(t *testing.T) {
	cases := map[string]struct {
		policy   string
		wantUses string
		atEnd    bool
		level    caps.Level
	}{
		"default":   {"", "actions/cache@", false, caps.Native},
		"pull-push": {"pull-push", "actions/cache@", false, caps.Native},
		"pull":      {"pull", "actions/cache/restore@", false, caps.Native},
		"push":      {"push", "actions/cache/save@", true, caps.Native},
		"unknown":   {"bogus", "actions/cache@", false, caps.Emulated},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := baseProject(t)
			job(p, "build").Cache = []ir.Cache{{Key: "deps-${{ sha }}", Policy: c.policy, Paths: []string{"~/.cache", "vendor"}}}
			files, rep, err := gha.New(gha.GHA).Emit(p)
			if err != nil {
				t.Fatal(err)
			}
			data := string(files[".github/workflows/ci.yml"])
			cacheAt := strings.Index(data, c.wantUses)
			runAt := strings.Index(data, "make build")
			if cacheAt < 0 || runAt < 0 {
				t.Fatalf("missing %q or the job script:\n%s", c.wantUses, data)
			}
			if (cacheAt > runAt) != c.atEnd {
				t.Errorf("cache step after the script = %v, want %v:\n%s", cacheAt > runAt, c.atEnd, data)
			}
			if !strings.Contains(data, "key: deps-${{ github.sha }}") || !strings.Contains(data, "~/.cache") {
				t.Errorf("key or paths missing:\n%s", data)
			}
			if got := entryLevel(rep, caps.JobCache, jobBuild); got != c.level {
				t.Errorf("level = %v, want %v", got, c.level)
			}
		})
	}
}

func TestCachePolicyRoundTrip(t *testing.T) {
	for _, policy := range []string{"", "pull", "push"} {
		t.Run("policy "+policy, func(t *testing.T) {
			p := baseProject(t)
			j := job(p, "build")
			steps := len(j.Steps)
			j.Cache = []ir.Cache{{Key: "k", Policy: policy, Paths: []string{"vendor"}}}
			back, text := viaGHA(t, p)
			bj := back.Pipelines[0].Jobs[0]
			if len(bj.Steps) != steps || len(bj.Cache) != 1 || bj.Cache[0].Policy != policy {
				t.Errorf("cache not restored: steps=%d cache=%+v\n%s", len(bj.Steps), bj.Cache, text)
			}
		})
	}
}
